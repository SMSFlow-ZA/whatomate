//go:build linux

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Walk from / using held directory FDs; every ancestor is root-owned and
// unwritable by nonroot. No symlink, hardlinked leaf or unchecked pycache path.
func privateParent(path string) (int, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, "", denied
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return -1, "", denied
	}
	for _, part := range parts[:len(parts)-1] {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return -1, "", denied
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&0022 != 0 {
			unix.Close(fd)
			return -1, "", denied
		}
	}
	return fd, parts[len(parts)-1], nil
}
func privateBytes(path string, limit int64) ([]byte, error) {
	parent, name, e := privateParent(path)
	if e != nil {
		return nil, denied
	}
	defer unix.Close(parent)
	fd, e := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, denied
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Nlink != 1 || st.Size > limit {
		return nil, denied
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, denied
	}
	return b, nil
}
func newReceipt(path string) (*os.File, error) {
	parent, name, e := privateParent(path)
	if e != nil {
		return nil, denied
	}
	defer unix.Close(parent)
	fd, e := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return nil, denied
	}
	f := os.NewFile(uintptr(fd), name)
	if f.Sync() != nil || unix.Fsync(parent) != nil {
		f.Close()
		return nil, denied
	}
	return f, nil
}
