//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func privateFixture(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root-private descriptor fixture")
	}
	dir, e := os.MkdirTemp("/root", "compatible-private-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestPrivateReservationIsCreateOnlyAndIndependentOfUmask(t *testing.T) {
	dir := privateFixture(t)
	path := filepath.Join(dir, "catalog.json")
	mask := syscall.Umask(0777)
	file, e := reserveOutput(path)
	syscall.Umask(mask)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private reservation inherited unsafe or unreadable umask")
	}
	if _, e = reserveOutput(path); e == nil {
		t.Fatal("existing reservation reused")
	}
}

func TestPrivateInputsRefuseLinksAndUnsafePermissions(t *testing.T) {
	dir := privateFixture(t)
	path := filepath.Join(dir, "owner.toml")
	if e := os.WriteFile(path, []byte("synthetic fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readPrivate(path); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(path, filepath.Join(dir, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := readPrivate(filepath.Join(dir, "link")); e == nil {
		t.Fatal("symlink read")
	}
	if e := os.Link(path, filepath.Join(dir, "hard")); e != nil {
		t.Fatal(e)
	}
	if _, e := readPrivate(path); e == nil {
		t.Fatal("multiply linked credential read")
	}
	os.Remove(filepath.Join(dir, "hard"))
	os.Chmod(path, 0644)
	if _, e := readPrivate(path); e == nil {
		t.Fatal("public credential mode accepted")
	}
}
