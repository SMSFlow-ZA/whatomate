package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"gorm.io/gorm"
)

type FileChange struct {
	Path   string
	Data   []byte
	Delete bool
}
type publicationFile struct {
	Root    int
	Path    string
	Prior   []byte
	Existed bool
}
type publicationJournal struct {
	ID    string
	Files []publicationFile
}

// RecoverMediaPublications reconciles durable preimages with SQL commit markers
// before serving requests, including lost COMMIT acknowledgements.
func RecoverMediaPublications(db *gorm.DB, roots []string) error {
	return withStorageLock(db, func(conn *gorm.DB) error { return recoverPublications(conn, roots) })
}

func ReadWithStorageLock(db *gorm.DB, roots []string, read func(*gorm.DB) error) error {
	return withStorageLock(db, func(conn *gorm.DB) error {
		if err := recoverPublications(conn, roots); err != nil {
			return err
		}
		return read(conn)
	})
}

func publicationRoots(roots []string) ([]string, error) {
	clean, err := storageRoots(roots)
	if err != nil {
		return nil, err
	}
	for _, path := range clean {
		if err := noSymlinks(path); err != nil {
			return nil, err
		}
	}
	return clean, nil
}

func syncDirectory(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func durableWrite(root *os.Root, path, temp string, data []byte) error {
	defer root.Remove(temp)
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(temp, path); err != nil {
		return err
	}
	return syncDirectory(root, filepath.Dir(path))
}

func recoverPublications(conn *gorm.DB, roots []string) error {
	clean, err := publicationRoots(roots)
	if err != nil {
		return err
	}
	if err := conn.Exec("CREATE TABLE IF NOT EXISTS whatomate_media_publications(id uuid PRIMARY KEY)").Error; err != nil {
		return err
	}
	journalRoot, err := os.OpenRoot(clean[0])
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer journalRoot.Close()
	directory, err := journalRoot.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".branding-publication-") && strings.HasSuffix(name, ".json.tmp") {
			id := strings.TrimSuffix(strings.TrimPrefix(name, ".branding-publication-"), ".json.tmp")
			if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id || !entry.Type().IsRegular() {
				return fmt.Errorf("invalid abandoned journal")
			}
			if err := journalRoot.Remove(name); err != nil {
				return err
			}
			if err := syncDirectory(journalRoot, "."); err != nil {
				return err
			}
			continue
		}
		if !strings.HasPrefix(name, ".branding-publication-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("invalid media publication journal")
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, ".branding-publication-"), ".json")
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return fmt.Errorf("invalid publication identifier")
		}
		f, err := journalRoot.Open(name)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, 128<<20))
		f.Close()
		if err != nil {
			return err
		}
		var journal publicationJournal
		if err := json.Unmarshal(data, &journal); err != nil || journal.ID != id || len(journal.Files) > 16 {
			return fmt.Errorf("invalid media publication journal")
		}
		var committed bool
		if err := conn.Raw("SELECT EXISTS(SELECT 1 FROM whatomate_media_publications WHERE id=?)", id).Scan(&committed).Error; err != nil {
			return err
		}
		for index, file := range journal.Files {
			if file.Root < 0 || file.Root >= len(clean) || file.Path == "." || !filepath.IsLocal(file.Path) {
				return fmt.Errorf("invalid publication destination")
			}
			if err := noSymlinks(filepath.Join(clean[file.Root], file.Path)); err != nil {
				return err
			}
			root, err := os.OpenRoot(clean[file.Root])
			if err != nil {
				return err
			}
			temp := filepath.Join(filepath.Dir(file.Path), fmt.Sprintf(".branding-%s-%d.tmp", id, index))
			if !committed {
				if file.Existed {
					err = durableWrite(root, file.Path, temp, file.Prior)
				} else {
					err = root.Remove(file.Path)
					if os.IsNotExist(err) {
						err = nil
					}
					if err == nil {
						err = syncDirectory(root, filepath.Dir(file.Path))
					}
				}
			}
			if err == nil {
				err = root.Remove(temp)
				if os.IsNotExist(err) {
					err = nil
				}
			}
			root.Close()
			if err != nil {
				return err
			}
		}
		// Delete the durable journal before deleting the transaction marker. Repeated
		// recovery writes preimages again safely if interrupted part way through.
		if err := journalRoot.Remove(name); err != nil {
			return err
		}
		if err := syncDirectory(journalRoot, "."); err != nil {
			return err
		}
		if err := conn.Exec("DELETE FROM whatomate_media_publications WHERE id=?", id).Error; err != nil {
			return err
		}
	}
	return conn.Exec("DELETE FROM whatomate_media_publications").Error
}

// Persist the preimage before replacing files. Committed cleanup failures leave
// recovery material and do not turn a published update into a failed save.
func WriteFilesWithCommit(db *gorm.DB, cfg config.PlanConfig, roots []string, changes []FileChange, publish func(*gorm.DB) error) error {
	return withStorageLock(db, func(conn *gorm.DB) error {
		if err := recoverPublications(conn, roots); err != nil {
			return err
		}
		clean, err := publicationRoots(roots)
		if err != nil {
			return err
		}
		effective, err := EffectiveLimits(conn, cfg)
		if err != nil {
			return err
		}
		if len(changes) > 16 {
			return fmt.Errorf("too many media changes")
		}
		journal := publicationJournal{ID: uuid.NewString()}
		seen := map[string]bool{}
		var delta int64
		for _, change := range changes {
			absolute, err := filepath.Abs(change.Path)
			if err != nil {
				return err
			}
			if seen[absolute] {
				return fmt.Errorf("duplicate media destination")
			}
			seen[absolute] = true
			rootIndex := -1
			for i, base := range clean {
				if base != absolute && within(base, absolute) {
					rootIndex = i
					break
				}
			}
			if rootIndex < 0 {
				return fmt.Errorf("media destination is outside storage roots")
			}
			if err := noSymlinks(absolute); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
				return err
			}
			relative, err := filepath.Rel(clean[rootIndex], absolute)
			if err != nil {
				return err
			}
			file := publicationFile{Root: rootIndex, Path: relative}
			if info, err := os.Lstat(absolute); err == nil {
				if !info.Mode().IsRegular() || info.Size() > 32<<20 {
					return fmt.Errorf("invalid or oversized prior media")
				}
				file.Prior, err = os.ReadFile(absolute)
				if err != nil {
					return err
				}
				file.Existed = true
			} else if !os.IsNotExist(err) {
				return err
			}
			delta -= int64(len(file.Prior))
			if !change.Delete {
				delta += int64(len(change.Data))
			}
			journal.Files = append(journal.Files, file)
		}
		if effective.Enabled {
			usage, err := StorageUsage(clean)
			if err != nil {
				return err
			}
			if delta > 0 && (usage > effective.MaxStorageBytes || delta > effective.MaxStorageBytes-usage) {
				return ErrStorageLimit
			}
		}
		if err := os.MkdirAll(clean[0], 0755); err != nil {
			return err
		}
		root, err := os.OpenRoot(clean[0])
		if err != nil {
			return err
		}
		defer root.Close()
		name := ".branding-publication-" + journal.ID + ".json"
		data, err := json.Marshal(journal)
		if err != nil {
			return err
		}
		if err := durableWrite(root, name, name+".tmp", data); err != nil {
			return err
		}
		var panicValue any
		writeErr := func() (result error) {
			defer func() { panicValue = recover() }()
			return conn.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("INSERT INTO whatomate_media_publications(id) VALUES (?)", journal.ID).Error; err != nil {
					return err
				}
				for index, change := range changes {
					file := journal.Files[index]
					target, err := os.OpenRoot(clean[file.Root])
					if err != nil {
						return err
					}
					if change.Delete {
						err = target.Remove(file.Path)
						if os.IsNotExist(err) {
							err = nil
						}
						if err == nil {
							err = syncDirectory(target, filepath.Dir(file.Path))
						}
					} else {
						err = durableWrite(target, file.Path, filepath.Join(filepath.Dir(file.Path), fmt.Sprintf(".branding-%s-%d.tmp", journal.ID, index)), change.Data)
					}
					target.Close()
					if err != nil {
						return err
					}
				}
				if publish != nil {
					return publish(tx)
				}
				return nil
			})
		}()
		var committed bool
		outcomeErr := conn.Raw("SELECT EXISTS(SELECT 1 FROM whatomate_media_publications WHERE id=?)", journal.ID).Scan(&committed).Error
		if outcomeErr != nil {
			return errors.Join(writeErr, outcomeErr)
		}
		recoveryErr := recoverPublications(conn, clean)
		if panicValue != nil {
			panic(panicValue)
		}
		if committed {
			return nil
		}
		return errors.Join(writeErr, recoveryErr)
	})
}
