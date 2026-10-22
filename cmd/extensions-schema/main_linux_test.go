//go:build linux

package main

import (
	"bytes"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"os"
	"path/filepath"
	"testing"
)

func privateFixture(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("offline CLI requires root")
	}
	root, e := os.MkdirTemp("/root", "schema-cli-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func TestPrivateConfigRefusesSymlinksPublicFilesAndEnvironmentOverrides(t *testing.T) {
	root := privateFixture(t)
	path := filepath.Join(root, "owner.toml")
	if e := os.WriteFile(path, []byte("[database]\nuser='synthetic-owner'\npassword='synthetic-file-password'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("WHATOMATE_DATABASE_PASSWORD", "synthetic-environment-override")
	c, e := privateConfig(path)
	if e != nil || c.Database.Password != "synthetic-file-password" {
		t.Fatal("config was not exclusively file-owned", e)
	}
	alias := filepath.Join(root, "alias.toml")
	if e = os.Symlink(path, alias); e != nil {
		t.Fatal(e)
	}
	if _, e = privateConfig(alias); e == nil {
		t.Fatal("symlink accepted")
	}
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = privateConfig(path); e == nil {
		t.Fatal("public owner credentials accepted")
	}
	if _, e = privateConfig("relative.toml"); e == nil {
		t.Fatal("relative config accepted")
	}
}

func TestUncompiledOfflineContractRefusesBeforeOutputOrDatabase(t *testing.T) {
	root := privateFixture(t)
	path, output := filepath.Join(root, "owner.toml"), filepath.Join(root, "catalog.json")
	text := "[database]\nhost='127.0.0.1'\nport=1\nuser='synthetic-owner'\npassword='synthetic-file-password'\nname='synthetic-schema-only'\nssl_mode='verify-full'\nschema_managed_externally=true\n[extensions]\nprotected_mode=true\n"
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
	oldR, oldS := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity = oldR; protectedschema.ExpectedCatalogSHA256 = oldS })
	protectedschema.ReleaseIdentity = ""
	protectedschema.ExpectedCatalogSHA256 = ""
	var stdout bytes.Buffer
	if e := run([]string{"-config", path, "-baseline", "-catalog-output", output}, &stdout); e == nil {
		t.Fatal("uncompiled installer accepted")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("invalid contract reserved output / attempted DB connection")
	}
	if stdout.Len() != 0 {
		t.Fatal("refusal claimed success")
	}
}
