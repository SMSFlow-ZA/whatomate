//go:build linux

package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Unlike killing the Docker CLI, an independently bounded force-remove followed
// by an exact-name inspection accounts for the owned signature probe on cancel.
func verifyDefinition(ctx context.Context, c configuration, file string, run func(context.Context, ...string) ([]byte, error)) (result error) {
	if file != "main.cvd" && file != "daily.cvd" && file != "bytecode.cvd" {
		return denied
	}
	cpu, err := c.Supervisor.CPUQuota()
	if err != nil {
		return denied
	}
	name := "er-doc-signature-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = run(cleanup, "rm", "--force", name)
		cancel()
		verify, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		remaining, err := run(verify, "ps", "--all", "--quiet", "--filter", "name=^/"+name+"$")
		if err != nil || len(bytes.TrimSpace(remaining)) != 0 {
			result = denied
		}
	}()
	check, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	output, err := run(check, "run", "--rm", "--name", name, "--pull=never", "--network", "none", "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "512m", "--memory-swap", "512m", "--cpus", cpu, "--pids-limit", "32", "--mount", "type=bind,src="+c.Supervisor.DefinitionsRoot+",dst=/defs,readonly", "--entrypoint", "/usr/bin/sigtool", c.Supervisor.Image, "--info", "/defs/"+file)
	if err != nil || check.Err() != nil || len(output) > 16384 || !bytes.Contains(output, []byte("Verification OK.")) {
		return denied
	}
	return nil
}

func definitionRunner(c configuration) func(context.Context, ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, c.Supervisor.DockerPath, args...).Output()
	}
}
