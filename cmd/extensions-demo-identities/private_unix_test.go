//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateCustodyCreateOnlyAndNoLinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("isolated Linux root fixture required")
	}
	dir, e := os.MkdirTemp("/root", "demo-identities-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	plan := filepath.Join(dir, "plan.json")
	if e = os.WriteFile(plan, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = readPrivate(plan); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link.json")
	if e = os.Symlink(plan, link); e != nil {
		t.Fatal(e)
	}
	if _, e = readPrivate(link); e == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(link)
	if e = os.Link(plan, link); e != nil {
		t.Fatal(e)
	}
	if _, e = readPrivate(plan); e == nil {
		t.Fatal("hardlink accepted")
	}
	os.Remove(link)
	os.Chmod(plan, 0644)
	if _, e = readPrivate(plan); e == nil {
		t.Fatal("public-readable secret accepted")
	}
	os.Chmod(plan, 0600)
	attempt := filepath.Join(dir, "attempt.json")
	if reserveAttempt(attempt, Result{}) != nil || reserveAttempt(attempt, Result{}) == nil {
		t.Fatal("attempt not create-only")
	}
	secret := filepath.Join(dir, "passwords.json")
	if writePassword(secret, []byte("private-fixture-value")) != nil || writePassword(secret, []byte("replacement")) == nil {
		t.Fatal("password file not create-only")
	}
	os.Chmod(dir, 0777)
	if _, e = readPrivate(plan); e == nil {
		t.Fatal("untrusted ancestor accepted")
	}
}
