package plan

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
	"github.com/shridarpatil/whatomate/internal/config"
	"gorm.io/gorm"
)

var ErrStorageLimit = errors.New("managed plan storage limit exceeded")

const storageLockID int64 = 0x57485354

// EffectiveLimits reads the allowances currently installed by the host. The
// absent-state fallback preserves standalone legacy callers; production startup
// always calls Install before serving requests. Existing managed state never
// accepts a disabled configuration or malformed/missing allowance.
func EffectiveLimits(db *gorm.DB, cfg config.PlanConfig) (config.PlanConfig, error) {
	var exists bool
	if err := db.Raw("SELECT to_regclass('whatomate_plan_state') IS NOT NULL").Scan(&exists).Error; err != nil {
		return cfg, err
	}
	if !exists {
		return cfg, nil
	}
	if !cfg.Enabled {
		return cfg, fmt.Errorf("managed storage requires enabled host configuration")
	}
	var state []struct {
		MaxUsers        int
		MaxNumbers      int
		MaxStorageBytes int64
		SupportEmail    string
	}
	if err := db.Raw("SELECT max_users,max_numbers,max_storage_bytes,support_email FROM whatomate_plan_state WHERE singleton=true").Scan(&state).Error; err != nil {
		return cfg, err
	}
	if len(state) != 1 || state[0].MaxUsers <= 0 || state[0].MaxNumbers <= 0 || state[0].MaxStorageBytes <= 0 {
		return cfg, fmt.Errorf("managed storage state is incomplete")
	}
	cfg.MaxUsers = state[0].MaxUsers
	cfg.MaxNumbers = state[0].MaxNumbers
	cfg.MaxStorageBytes = state[0].MaxStorageBytes
	cfg.SupportEmail = state[0].SupportEmail
	return cfg, nil
}

func withStorageLock(db *gorm.DB, run func(*gorm.DB) error) error {
	if db == nil {
		return fmt.Errorf("storage synchronization requires a database")
	}
	return db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SELECT pg_advisory_lock(?)", storageLockID).Error; err != nil {
			return err
		}
		defer conn.WithContext(context.Background()).Exec("SELECT pg_advisory_unlock(?)", storageLockID)
		return run(conn)
	})
}

// UsageSnapshot excludes in-progress replacement/rollback files by sharing the
// writers' lock through their cleanup. Installed limits can change only outside
// this lock, so a stale process reports the same allowance its writes enforce.
func UsageSnapshot(db *gorm.DB, cfg config.PlanConfig, roots []string) (effective config.PlanConfig, users, numbers, bytes int64, err error) {
	effective = cfg
	err = withStorageLock(db, func(conn *gorm.DB) error {
		var e error
		effective, e = EffectiveLimits(conn, cfg)
		if e != nil {
			return e
		}
		users, numbers, e = Usage(conn, effective)
		if e != nil {
			return e
		}
		bytes, e = StorageUsage(roots)
		return e
	})
	return
}

// WriteFile serializes persistent media writes across all workers sharing a database.
// Existing files are adopted by scanning both storage roots, including overwrite deltas.
func WriteFile(db *gorm.DB, cfg config.PlanConfig, roots []string, path string, data []byte) error {
	return WriteFileWithCommit(db, cfg, roots, path, data, nil)
}

// WriteFileWithCommit keeps the storage lock through publication and filesystem
// recovery. The optional callback uses the same database transaction: provider
// rejection or SQL/commit failure restores the previous bytes and metadata before
// another writer proceeds. Callbacks must use bounded provider request contexts.
func WriteFileWithCommit(db *gorm.DB, cfg config.PlanConfig, roots []string, path string, data []byte, publish func(*gorm.DB) error) error {
	if !cfg.Enabled && db == nil && publish == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		return atomicWrite(path, data)
	}
	if db == nil || (cfg.Enabled && cfg.MaxStorageBytes <= 0) {
		return fmt.Errorf("managed storage requires a database and positive limit")
	}
	// Pin a connection and retain its session lock until filesystem rollback and
	// cleanup finish. The transaction lock alone releases at COMMIT, which would
	// permit another writer to race a rollback after a failed database commit.
	return withStorageLock(db, func(conn *gorm.DB) error {
		effective, err := EffectiveLimits(conn, cfg)
		if err != nil {
			return err
		}
		var root *os.Root
		var relative, backup string
		var installed bool
		defer func() {
			if root != nil {
				_ = root.Close()
			}
		}()
		writeErr := conn.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", storageLockID).Error; err != nil {
				return err
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			rootPath := filepath.Dir(absolute)
			if effective.Enabled {
				cleanRoots, err := storageRoots(roots)
				if err != nil {
					return err
				}
				rootPath = ""
				for _, root := range cleanRoots {
					if within(root, absolute) && root != absolute {
						rootPath = root
						break
					}
				}
				if rootPath == "" {
					return fmt.Errorf("media destination is outside storage roots")
				}
				if err := noSymlinks(absolute); err != nil {
					return err
				}
				usage, err := StorageUsage(cleanRoots)
				if err != nil {
					return fmt.Errorf("cannot determine storage usage: %w", err)
				}
				var prior int64
				if info, err := os.Lstat(absolute); err == nil {
					if !info.Mode().IsRegular() {
						return fmt.Errorf("media destination is not a regular file")
					}
					prior = info.Size()
				} else if !os.IsNotExist(err) {
					return err
				}
				delta := int64(len(data)) - prior
				// Reductions and equal-sized replacements remain possible after a downgrade.
				if delta > 0 && (usage > effective.MaxStorageBytes || delta > effective.MaxStorageBytes-usage) {
					return ErrStorageLimit
				}
			}
			if err := os.MkdirAll(rootPath, 0755); err != nil {
				return err
			}
			root, err = os.OpenRoot(rootPath)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(rootPath, absolute)
			if err != nil {
				return err
			}
			if err := root.MkdirAll(filepath.Dir(rel), 0755); err != nil {
				return err
			}
			relative = rel
			backup, err = backupPriorFile(root, rel)
			if err != nil {
				return err
			}
			err = atomicRootWrite(root, rel, data)
			installed = err == nil
			if err == nil && publish != nil {
				return publish(tx)
			}
			return err
		})
		if writeErr != nil && installed {
			var restoreErr error
			if backup != "" {
				restoreErr = root.Rename(backup, relative)
			} else {
				restoreErr = root.Remove(relative)
			}
			if restoreErr != nil {
				return fmt.Errorf("%w; previous media recovery failed: %v", writeErr, restoreErr)
			}
		}
		if backup != "" {
			_ = root.Remove(backup)
		}
		return writeErr
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
