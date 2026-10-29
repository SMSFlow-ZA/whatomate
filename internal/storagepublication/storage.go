package storagepublication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrStorageLimit = errors.New("storage publication limit exceeded")

const storageLockID int64 = 0x57485354

func withStorageLock(db *gorm.DB, run func(*gorm.DB) error) error {
	if db == nil {
		return fmt.Errorf("storage synchronization requires a database")
	}
	return db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SELECT pg_advisory_lock(?)", storageLockID).Error; err != nil {
			return err
		}
		defer conn.WithContext(context.Background()).Exec("SELECT pg_advisory_unlock(?)", storageLockID)
		// Connection supplies a mutable statement. Start a fresh ORM session on the
		// same pinned SQL connection so quota scans and missing-row reads cannot
		// contaminate subsequent publication queries or the advisory unlock.
		return run(conn.Session(&gorm.Session{NewDB: true}))
	})
}

func backupPriorFile(root *os.Root, path string) (string, error) {
	previous, err := root.Open(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = previous.Close() }()
	backup := filepath.Join(filepath.Dir(path), ".media-"+uuid.NewString()+".rollback")
	f, err := root.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(f, previous); err != nil {
		_ = f.Close()
		_ = root.Remove(backup)
		return "", err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = root.Remove(backup)
		return "", err
	}
	if err = f.Close(); err != nil {
		_ = root.Remove(backup)
		return "", err
	}
	return backup, nil
}

// StorageUsage counts regular files once across possibly overlapping roots.
// Missing roots are empty; inaccessible paths and symbolic links fail closed.
func StorageUsage(roots []string) (int64, error) {
	cleanRoots, err := storageRoots(roots)
	if err != nil {
		return 0, err
	}
	var size int64
	for _, path := range cleanRoots {
		if err := noSymlinks(path); err != nil {
			return 0, err
		}
		root, err := os.OpenRoot(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic links are not supported in storage")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("non-regular files are not supported in storage")
			}
			if info.Size() > math.MaxInt64-size {
				return fmt.Errorf("storage usage overflow")
			}
			size += info.Size()
			return nil
		})
		_ = root.Close()
		if err != nil {
			return 0, err
		}
	}
	return size, nil
}

func storageRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("no storage roots configured")
	}
	var paths []string
	for _, root := range roots {
		if root == "" {
			return nil, fmt.Errorf("empty storage root")
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		paths = append(paths, abs)
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) < len(paths[j]) })
	var result []string
	for _, path := range paths {
		covered := false
		for _, root := range result {
			if within(root, path) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, path)
		}
	}
	return result, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

// Check the whole ancestor chain, including a configured root's parents.
func noSymlinks(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported in storage")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func atomicWrite(path string, data []byte) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return atomicRootWrite(root, filepath.Base(path), data)
}

func atomicRootWrite(root *os.Root, path string, data []byte) error {
	temp := filepath.Join(filepath.Dir(path), ".media-"+uuid.NewString()+".tmp")
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temp) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return root.Rename(temp, path)
}
