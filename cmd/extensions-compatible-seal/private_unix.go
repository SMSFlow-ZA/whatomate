//go:build !windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func ancestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || os.Geteuid() != 0 {
		return refused
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil {
			return refused
		}
		s, ok := info.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != 0 || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return refused
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

func privateStat(path string) (os.FileInfo, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, refused
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != 0 || s.Gid != 0 || s.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return nil, refused
	}
	return info, nil
}

func readPrivate(path string) ([]byte, error) {
	if ancestors(path) != nil {
		return nil, refused
	}
	before, e := privateStat(path)
	if e != nil {
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
	raw, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return nil, refused
	}
	final, e := privateStat(path)
	actual, e2 := f.Stat()
	if e != nil || e2 != nil || !os.SameFile(before, final) || !os.SameFile(before, actual) || before.Size() != actual.Size() || !before.ModTime().Equal(actual.ModTime()) {
		return nil, refused
	}
	return raw, nil
}

func validateOutput(path string) error {
	if ancestors(path) != nil {
		return refused
	}
	if _, e := os.Lstat(path); !os.IsNotExist(e) {
		return refused
	}
	return nil
}

func reserveOutput(path string) (*os.File, error) {
	if validateOutput(path) != nil {
		return nil, refused
	}
	fd, e := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, refused
	}
	f := os.NewFile(uintptr(fd), path)
	if f.Chmod(0600) != nil || finishOutput(path, f) != nil {
		f.Close()
		return nil, refused
	}
	if f.Sync() != nil {
		f.Close()
		return nil, refused
	}
	parent, e := os.Open(filepath.Dir(path))
	if e != nil {
		f.Close()
		return nil, refused
	}
	defer parent.Close()
	if parent.Sync() != nil {
		f.Close()
		return nil, refused
	}
	return f, nil
}

func finishOutput(path string, file *os.File) error {
	actual, e := file.Stat()
	if e != nil {
		return refused
	}
	info, e := os.Lstat(path)
	if e != nil || !os.SameFile(actual, info) {
		return refused
	}
	stat, ok := actual.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || actual.Mode().Perm() != 0600 {
		return refused
	}
	parent, e := os.Open(filepath.Dir(path))
	if e != nil {
		return refused
	}
	defer parent.Close()
	return parent.Sync()
}
