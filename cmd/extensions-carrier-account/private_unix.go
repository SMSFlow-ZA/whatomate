//go:build !windows

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func privateInfo(path string, directory bool) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || os.Geteuid() != 0 {
		return nil, refused
	}
	info, e := os.Lstat(path)
	if e != nil {
		return nil, refused
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || directory && info.Mode().Perm()&0022 != 0 || !directory && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || s.Nlink != 1) {
		return nil, refused
	}
	return info, nil
}
func ancestors(path string) error {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if _, e := privateInfo(p, true); e != nil {
			return e
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}
func readPrivate(path string) ([]byte, error) {
	if ancestors(path) != nil {
		return nil, refused
	}
	before, e := privateInfo(path, false)
	if e != nil || before.Size() > 1<<20 {
		return nil, refused
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, refused
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		return nil, refused
	}
	data, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(data) > 1<<20 {
		return nil, refused
	}
	return data, nil
}
func reserveAttempt(path string, r Result) error {
	if ancestors(path) != nil {
		return refused
	}
	fd, e := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return refused
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	e = json.NewEncoder(f).Encode(struct {
		Result
		AutomaticRetry bool   `json:"automatic_retry"`
		Outcome        string `json:"outcome"`
	}{r, false, "reserved_inspect_database_before_reconciliation"})
	if e != nil {
		return refused
	}
	if f.Sync() != nil {
		return refused
	}
	parent, e := os.Open(filepath.Dir(path))
	if e != nil {
		return refused
	}
	defer parent.Close()
	if parent.Sync() != nil {
		return refused
	}
	return nil
}
