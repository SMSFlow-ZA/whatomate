package tts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// PiperTTS generates OGG/Opus audio files from text using the Piper TTS CLI
// and opusenc for WAV→OGG/Opus conversion. No cgo dependency required.
type PiperTTS struct {
	BinaryPath    string                               // path to piper executable
	ModelPath     string                               // path to .onnx model file
	OpusencBinary string                               // path to opusenc (defaults to "opusenc")
	AudioDir      string                               // output directory for generated files
	Store         func(path string, data []byte) error // optional persistent storage guard
}

// Generate converts text to an OGG/Opus audio file. Returns the filename.
// Uses SHA256 hash of text as the filename for caching — same text produces the same file.
func (p *PiperTTS) Generate(text string) (string, error) {
	hash := sha256Short(text)
	filename := "tts_" + hash + ".ogg"
	outPath := filepath.Join(p.AudioDir, filename)

	// Cache hit — file already exists
	if fileExists(outPath) {
		if p.Store != nil {
			data, err := readGeneratedAudio(outPath)
			if err != nil {
				return "", err
			}
			if err := p.Store(outPath, data); err != nil {
				return "", err
			}
		}
		return filename, nil
	}

	// Ensure output directory exists
	if err := os.MkdirAll(p.AudioDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create audio directory: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Conversion outputs belong in runtime temporary storage, never in the
	// persistent audio root. Unique directories also isolate concurrent requests.
	tempDir, err := os.MkdirTemp("", "whatomate-tts-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	wavPath := filepath.Join(tempDir, "speech.wav")

	piperCmd := exec.CommandContext(ctx, p.BinaryPath,
		"--model", p.ModelPath,
		"--output_file", wavPath,
		"--length_scale", "1.0",
	)
	piperCmd.Stdin = bytes.NewReader([]byte(text))

	var piperStderr bytes.Buffer
	piperCmd.Stderr = &piperStderr

	if err := runBounded(ctx, cancel, piperCmd, tempDir); err != nil {
		return "", fmt.Errorf("piper TTS failed: %w (stderr: %s)", err, piperStderr.String())
	}

	// Verify WAV was created
	if !fileExists(wavPath) {
		return "", fmt.Errorf("piper did not produce output file")
	}

	// Step 2: Convert WAV → OGG/Opus using opusenc
	opusenc := p.OpusencBinary
	if opusenc == "" {
		opusenc = "opusenc"
	}

	// Write to a temp file first, then rename for atomicity
	tmpOgg := filepath.Join(tempDir, "speech.ogg")

	encCmd := exec.CommandContext(ctx, opusenc,
		"--bitrate", "24",
		"--quiet",
		wavPath,
		tmpOgg,
	)

	var encStderr bytes.Buffer
	encCmd.Stderr = &encStderr

	if err := runBounded(ctx, cancel, encCmd, tempDir); err != nil {
		return "", fmt.Errorf("opusenc failed: %w (stderr: %s)", err, encStderr.String())
	}

	data, err := readGeneratedAudio(tmpOgg)
	if err != nil {
		return "", err
	}
	store := p.Store
	if store == nil {
		store = storeAudio
	}
	if err := store(outPath, data); err != nil {
		return "", fmt.Errorf("failed to finalize audio file: %w", err)
	}

	return filename, nil
}

// sha256Short returns the first 16 hex characters of the SHA256 hash of s.
func sha256Short(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}

// fileExists returns true if the file at path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

const maxGeneratedAudioBytes = 16 << 20

func readGeneratedAudio(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxGeneratedAudioBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxGeneratedAudioBytes {
		return nil, fmt.Errorf("generated audio exceeds 16MB")
	}
	return data, nil
}

func storeAudio(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tts-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Stop runaway external converters before their temporary output grows without
// bound. A runtime tmpfs size limit remains the final filesystem-level bound.
func runBounded(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, dir string) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				return err
			}
			return checkTemporaryAudioSize(dir)
		case <-ctx.Done():
			return <-done
		case <-ticker.C:
			if err := checkTemporaryAudioSize(dir); err != nil {
				cancel()
				<-done
				return err
			}
		}
	}
}

func checkTemporaryAudioSize(dir string) error {
	var size int64
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	if size > 2*maxGeneratedAudioBytes {
		return fmt.Errorf("TTS temporary output limit exceeded")
	}
	return nil
}
