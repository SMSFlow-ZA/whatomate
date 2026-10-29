package tts_test

import (
	"github.com/shridarpatil/whatomate/internal/tts"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPiperConcurrentIdenticalRequestsPublishOneReusableResult(t *testing.T) {
	dir := t.TempDir()
	p := &tts.PiperTTS{AudioDir: dir, ModelPath: "model", BinaryPath: fakeBin(t, "piper", `
while [ "$#" -gt 0 ]; do
 case "$1" in --output_file) out="$2";shift 2;;*) shift;;esac
done
cat > /dev/null
sleep 0.05
printf RIFF > "$out"
`), OpusencBinary: fakeBin(t, "opusenc", `
prev="";last=""
for a in "$@";do prev="$last";last="$a";done
sleep 0.05
test -f "$prev" || exit 3
printf OggS > "$last"
`)}
	start := make(chan struct{})
	results := make(chan error, 8)
	names := make(chan string, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			name, err := p.Generate("Concurrent example")
			results <- err
			names <- name
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(names)
	for err := range results {
		require.NoError(t, err)
	}
	var expected string
	for name := range names {
		if expected == "" {
			expected = name
		}
		require.Equal(t, expected, name)
	}
	data, err := os.ReadFile(filepath.Join(dir, expected))
	require.NoError(t, err)
	require.Equal(t, "OggS", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
