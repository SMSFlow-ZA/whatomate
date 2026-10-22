package main

import (
	"github.com/shridarpatil/whatomate/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Invoke the real entry point with its default -workers=1. The database is
// deliberately unreachable: configuration refusal must precede any connection,
// migration, irreversible activation or background worker.
func TestServerProtectedDefaultWorkersRefusedBeforeDatabase(t *testing.T) {
	if os.Getenv("WHATOMATE_PREFLIGHT_CHILD") == "1" {
		runServer([]string{"-config", os.Getenv("WHATOMATE_PREFLIGHT_CONFIG"), "-migrate"})
		return
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[extensions]\nprotected_mode = true\n[database]\nhost = \"127.0.0.1\"\nport = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestServerProtectedDefaultWorkersRefusedBeforeDatabase$")
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "WHATOMATE_") {
			command.Env = append(command.Env, item)
		}
	}
	command.Env = append(command.Env, "WHATOMATE_PREFLIGHT_CHILD=1", "WHATOMATE_PREFLIGHT_CONFIG="+path)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "server worker configuration requires -workers 0") {
		t.Fatalf("protected default workers were not refused before database access: %s (%v)", output, err)
	}
	if strings.Contains(string(output), "Connected to PostgreSQL") {
		t.Fatal("startup preflight occurred after database initialization")
	}
}

func TestServerWorkerPreflightPreservesExplicitOrdinaryAndProtectedModes(t *testing.T) {
	ordinary := &config.Config{}
	if err := preflightServerWorkers(ordinary, 1); err != nil {
		t.Fatalf("ordinary default rejected: %v", err)
	}
	if err := preflightServerWorkers(ordinary, -1); err == nil {
		t.Fatal("negative worker count accepted")
	}
	for _, extensions := range []config.ExtensionsConfig{
		{ProtectedMode: true}, {Modules: "cases"}, {RequiredInbound: "directory"}, {CarrierEnabled: true}, {CarrierDispatchEnabled: true}, {CarrierSMSBindings: []config.ExtensionCarrierSMSBinding{{ConnectionID: "operator-bound"}}},
	} {
		c := &config.Config{Extensions: extensions}
		if err := preflightServerWorkers(c, 1); err == nil {
			t.Fatal("unsupported generic worker accepted")
		}
		if err := preflightServerWorkers(c, 0); err != nil {
			t.Fatalf("explicit dedicated server worker mode rejected: %v", err)
		}
	}
}
