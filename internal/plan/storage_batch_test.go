package plan

import (
	"errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestStorageBatchQuotaAndRollback(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(1, 1)
	cfg.MaxStorageBytes = 8
	require.NoError(t, Install(db, cfg))
	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.png")
	icon := filepath.Join(dir, "favicon.png")
	require.NoError(t, os.WriteFile(logo, []byte("old"), 0644))
	require.NoError(t, os.WriteFile(icon, []byte("old"), 0644))
	require.NoError(t, db.Exec("CREATE TABLE branding_test(value text); INSERT INTO branding_test VALUES ('old')").Error)
	changes := []FileChange{{Path: logo, Data: []byte("logo")}, {Path: icon, Data: []byte("icon")}}
	err := WriteFilesWithCommit(db, cfg, []string{dir}, changes, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("UPDATE branding_test SET value='bad'").Error)
		return errors.New("rejected")
	})
	require.ErrorContains(t, err, "rejected")
	for _, path := range []string{logo, icon} {
		b, e := os.ReadFile(path)
		require.NoError(t, e)
		require.Equal(t, "old", string(b))
	}
	var value string
	require.NoError(t, db.Raw("SELECT value FROM branding_test").Scan(&value).Error)
	require.Equal(t, "old", value)
	require.NoError(t, WriteFilesWithCommit(db, cfg, []string{dir}, changes, nil))
	require.ErrorIs(t, WriteFilesWithCommit(db, cfg, []string{dir}, []FileChange{{Path: logo, Data: []byte("larger")}}, nil), ErrStorageLimit)
	require.NoError(t, WriteFilesWithCommit(db, cfg, []string{dir}, []FileChange{{Path: logo, Delete: true}, {Path: icon, Delete: true}}, nil))
	entries, e := os.ReadDir(dir)
	require.NoError(t, e)
	require.Empty(t, entries)
}

func TestStorageBatchProcessDeath(t *testing.T) {
	if os.Getenv("BRANDING_CRASH_CHILD") == "1" {
		db, err := gorm.Open(postgres.Open(os.Getenv("BRANDING_CRASH_DSN")), &gorm.Config{})
		require.NoError(t, err)
		root := os.Getenv("BRANDING_CRASH_ROOT")
		_ = WriteFilesWithCommit(db, quotaConfig(1, 1), []string{root}, []FileChange{{Path: filepath.Join(root, "logo.png"), Data: []byte("new")}, {Path: filepath.Join(root, "favicon.png"), Delete: true}}, func(tx *gorm.DB) error {
			if tx.Exec("UPDATE branding_test SET value='new'").Error != nil {
				os.Exit(9)
			}
			os.Exit(7)
			return nil
		})
		os.Exit(8)
	}
	db := quotaDB(t)
	root := t.TempDir()
	require.NoError(t, db.Exec("CREATE TABLE branding_test(value text); INSERT INTO branding_test VALUES ('old')").Error)
	for _, name := range []string{"logo.png", "favicon.png"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("old"), 0644))
	}
	command := exec.Command(os.Args[0], "-test.run=^TestStorageBatchProcessDeath$")
	command.Env = append(os.Environ(), "BRANDING_CRASH_CHILD=1", "BRANDING_CRASH_ROOT="+root, "BRANDING_CRASH_DSN="+db.Dialector.(*postgres.Dialector).Config.DSN)
	err := command.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 7, exit.ExitCode())
	require.NoError(t, RecoverMediaPublications(db, []string{root}))
	for _, name := range []string{"logo.png", "favicon.png"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, "old", string(data))
	}
	var value string
	require.NoError(t, db.Raw("SELECT value FROM branding_test").Scan(&value).Error)
	require.Equal(t, "old", value)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestStorageBatchCommittedOutcomeAndDeferredCleanup(t *testing.T) {
	db := quotaDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "logo.png")
	// The publication really commits, but the surrounding transaction receives an
	// error instead of a successful acknowledgement. Recovery must retain it.
	require.NoError(t, WriteFilesWithCommit(db, quotaConfig(1, 1), []string{root}, []FileChange{{Path: path, Data: []byte("committed")}}, func(tx *gorm.DB) error { return tx.Commit().Error }))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "committed", string(data))
	// Simulate cleanup being unavailable after successful publication. A later
	// recovery retries it; the save still succeeds and keeps the published bytes.
	obstruction := filepath.Join(root, ".branding-publication-invalid.json")
	require.NoError(t, WriteFilesWithCommit(db, quotaConfig(1, 1), []string{root}, []FileChange{{Path: path, Data: []byte("published")}}, func(tx *gorm.DB) error { return os.WriteFile(obstruction, []byte("unavailable"), 0600) }))
	require.NoError(t, os.Remove(obstruction))
	require.NoError(t, RecoverMediaPublications(db, []string{root}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "published", string(data))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Panics(t, func() {
		_ = WriteFilesWithCommit(db, quotaConfig(1, 1), []string{root}, []FileChange{{Path: path, Delete: true}}, func(tx *gorm.DB) error { panic("interrupted") })
	})
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "published", string(data))
}

func TestStoragePublicationAbandonedJournalAndWriteFailure(t *testing.T) {
	db := quotaDB(t)
	root := t.TempDir()
	abandoned := filepath.Join(root, ".branding-publication-"+uuid.NewString()+".json.tmp")
	require.NoError(t, os.WriteFile(abandoned, []byte("interrupted before promotion"), 0600))
	require.NoError(t, RecoverMediaPublications(db, []string{root}))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, os.Mkdir(filepath.Join(root, "cannot-replace-directory"), 0700))
	handle, err := os.OpenRoot(root)
	require.NoError(t, err)
	defer handle.Close()
	require.Error(t, durableWrite(handle, "cannot-replace-directory", "failed.tmp", []byte("new")))
	_, err = os.Stat(filepath.Join(root, "failed.tmp"))
	require.True(t, os.IsNotExist(err))
}

func TestStorageBatchConcurrentPublications(t *testing.T) {
	db := quotaDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "logo.png")
	require.NoError(t, db.Exec("CREATE TABLE branding_test(value text); INSERT INTO branding_test VALUES ('old')").Error)
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(value string) {
			defer group.Done()
			failures <- WriteFilesWithCommit(db, quotaConfig(1, 1), []string{root}, []FileChange{{Path: path, Data: []byte(value)}}, func(tx *gorm.DB) error { return tx.Exec("UPDATE branding_test SET value=?", value).Error })
		}(uuid.NewString())
	}
	group.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var value string
	require.NoError(t, db.Raw("SELECT value FROM branding_test").Scan(&value).Error)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, value, string(data))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
