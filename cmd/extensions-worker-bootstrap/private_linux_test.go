//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func privateFixture(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("explicit Linux root descriptor fixture required")
	}
	root, e := os.MkdirTemp("/run", "worker-bootstrap-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}
func TestLinuxPrivateFDAndCreateOnlyReceipt(t *testing.T) {
	root := privateFixture(t)
	file := filepath.Join(root, "plan.json")
	if e := os.WriteFile(file, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := privateBytes(file, 2); e != nil {
		t.Fatal("private read refused")
	}
	if _, e := privateBytes(file, 1); e == nil {
		t.Fatal("reader bound bypassed")
	}
	if e := os.Link(file, filepath.Join(root, "hard.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := privateBytes(file, 2); e == nil {
		t.Fatal("hardlink accepted")
	}
	os.Remove(filepath.Join(root, "hard.json"))
	if e := os.Symlink(file, filepath.Join(root, "link.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := privateBytes(filepath.Join(root, "link.json"), 2); e == nil {
		t.Fatal("symlink accepted")
	}
	receipt := filepath.Join(root, "outcome.json")
	f, e := newReceipt(receipt)
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	if _, e = newReceipt(receipt); e == nil {
		t.Fatal("receipt overwritten")
	}
	if _, e = privateBytes(receipt, 0); e != nil {
		t.Fatal("create-only receipt not private")
	}
}

func TestPostgresTLSVerifyFullAndExactCLIReceipt(t *testing.T) {
	tlsPath := os.Getenv("WORKER_BOOTSTRAP_TEST_TLS")
	if tlsPath == "" {
		t.Skip("explicit synthetic local TLS fixture required")
	}
	db := disposable(t)
	p := prepareFixture(t, db)
	db.Close() // owner pool must be exclusive
	root := privateFixture(t)
	ca, e := os.ReadFile(filepath.Join(tlsPath, "server.crt"))
	if e != nil {
		t.Fatal(e)
	}
	caFile := filepath.Join(root, "ca.crt")
	passwordFile := filepath.Join(root, "password")
	os.WriteFile(caFile, ca, 0600)
	os.WriteFile(passwordFile, []byte(fixturePassword), 0600)
	database, owner, _, _ := p.names()
	c := Connection{Version: 1, Host: "localhost", Port: 5432, Database: database, User: owner, SSLMode: "verify-full", CAFile: caFile, PasswordFile: passwordFile, TimeoutSeconds: 30}
	s, e := openOwner(context.Background(), c, p)
	if e != nil {
		t.Fatal("actual verify-full refused")
	}
	s.DB.Close()
	wrong := c
	wrong.Host = "127.0.0.1"
	if bad, e := openOwner(context.Background(), wrong, p); e == nil {
		bad.DB.Close()
		t.Fatal("wrong certificate hostname admitted")
	}
	wrong = c
	wrong.SSLMode = "require"
	if bad, e := openOwner(context.Background(), wrong, p); e == nil {
		bad.DB.Close()
		t.Fatal("unverified TLS admitted")
	}
	raw, _ := json.Marshal(p)
	planFile := filepath.Join(root, "plan.json")
	os.WriteFile(planFile, raw, 0600)
	raw, _ = json.Marshal(c)
	connectionFile := filepath.Join(root, "connection.json")
	os.WriteFile(connectionFile, raw, 0600)
	SourceRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	outcome := filepath.Join(root, "receipt.json")
	if e = run([]string{"-connection", connectionFile, "-plan", planFile, "-mode", "apply", "-receipt", outcome}, os.Stdout, os.Stderr); e != nil {
		t.Fatal("real TLS CLI apply refused")
	}
	if e = run([]string{"-connection", connectionFile, "-plan", planFile, "-mode", "apply", "-receipt", outcome}, os.Stdout, os.Stderr); e == nil {
		t.Fatal("immutable attempt replayed")
	}
	receipt, e := privateBytes(outcome, 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	var envelope struct {
		Result Result `json:"result"`
	}
	if json.Unmarshal(receipt, &envelope) != nil || envelope.Result.Outcome != "committed-observed" || envelope.Result.PlanSHA256 != digest(mustRead(t, planFile)) {
		t.Fatal("CLI raw plan/commit receipt mismatch")
	}
}
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
