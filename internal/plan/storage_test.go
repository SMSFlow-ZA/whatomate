package plan_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func storageDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// Existing files and both configured roots must consume the same allowance.
func TestWriteFileCountsExistingFilesAndPreservesDeniedOverwrite(t *testing.T) {
	db := storageDB(t)
	uploads, audio := t.TempDir(), t.TempDir()
	roots := []string{uploads, audio}
	existing := filepath.Join(uploads, "old.bin")
	require.NoError(t, os.WriteFile(existing, []byte("12345"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(audio, "hold.ogg"), []byte("123"), 0644))
	cfg := config.PlanConfig{Enabled: true, MaxStorageBytes: 10}
	require.NoError(t, plan.WriteFile(db, cfg, roots, existing, []byte("1234567")))
	err := plan.WriteFile(db, cfg, roots, existing, []byte("12345678"))
	require.ErrorIs(t, err, plan.ErrStorageLimit)
	got, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "1234567", string(got))
	usage, err := plan.StorageUsage(roots)
	require.NoError(t, err)
	require.Equal(t, int64(10), usage)
	cfg.MaxStorageBytes = 5
	require.NoError(t, plan.WriteFile(db, cfg, roots, existing, []byte("12")), "shrinking an over-limit instance remains possible")
	require.ErrorIs(t, plan.WriteFile(db, cfg, roots, filepath.Join(audio, "new.ogg"), []byte("x")), plan.ErrStorageLimit)
	entries, err := os.ReadDir(uploads)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary files survive")
}

func TestWriteFileSerializesFinalBytes(t *testing.T) {
	db := storageDB(t)
	dir := t.TempDir()
	cfg := config.PlanConfig{Enabled: true, MaxStorageBytes: 4}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- plan.WriteFile(db, cfg, []string{dir}, filepath.Join(dir, fmt.Sprintf("%d.bin", i)), []byte("1234"))
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.True(t, errors.Is(err, plan.ErrStorageLimit), "unexpected error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	usage, err := plan.StorageUsage([]string{dir})
	require.NoError(t, err)
	require.Equal(t, int64(4), usage)
}

func TestStorageRejectsSymlinksOutOfRootAndScanFailures(t *testing.T) {
	db := storageDB(t)
	dir := t.TempDir()
	outside := t.TempDir()
	cfg := config.PlanConfig{Enabled: true, MaxStorageBytes: 100}
	require.Error(t, plan.WriteFile(db, cfg, []string{dir}, filepath.Join(outside, "escape"), []byte("bad")))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(outside, link))
	require.Error(t, plan.WriteFile(db, cfg, []string{dir}, filepath.Join(link, "escape"), []byte("bad")))
	_, err := plan.StorageUsage([]string{dir})
	require.Error(t, err)
	require.NoError(t, os.Remove(link))
	badRoot := filepath.Join(outside, "regular")
	require.NoError(t, os.WriteFile(badRoot, []byte("x"), 0644))
	require.Error(t, plan.WriteFile(db, cfg, []string{dir, badRoot}, filepath.Join(dir, "valid"), []byte("bad")))
	_, err = os.Stat(filepath.Join(dir, "valid"))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, os.Symlink(dir, filepath.Join(outside, "rootlink")))
	require.Error(t, plan.WriteFile(db, cfg, []string{filepath.Join(outside, "rootlink")}, filepath.Join(dir, "new"), []byte("bad")))
}

func TestStorageUsageDeduplicatesRootsAndAcceptsMissingRoots(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested")
	require.NoError(t, os.Mkdir(sub, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "old"), []byte("12345"), 0644))
	usage, err := plan.StorageUsage([]string{dir, dir, sub, filepath.Join(t.TempDir(), "missing")})
	require.NoError(t, err)
	require.Equal(t, int64(5), usage)
}

func TestWriteFileFailsClosedWithoutDatabaseOrPositiveLimit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new")
	require.Error(t, plan.WriteFile(nil, config.PlanConfig{Enabled: true, MaxStorageBytes: 10}, []string{dir}, target, []byte("x")))
	require.Error(t, plan.WriteFile(storageDB(t), config.PlanConfig{Enabled: true}, []string{dir}, target, []byte("x")))
	require.NoError(t, plan.WriteFile(nil, config.PlanConfig{}, []string{dir}, target, []byte("unmanaged")))
}

func TestWriteFileRestoresPreviousFileWhenDatabaseCommitFails(t *testing.T) {
	db := storageDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	schema := "storage_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		_ = db.Exec("SET search_path TO public").Error
		_ = db.Exec("DROP SCHEMA " + schema + " CASCADE").Error
	})
	require.NoError(t, db.Exec("CREATE TABLE "+schema+".parents(id int PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE "+schema+".children(parent_id int REFERENCES "+schema+".parents(id) DEFERRABLE INITIALLY DEFERRED)").Error)
	// A real deferred constraint makes COMMIT fail after the filesystem write.
	require.NoError(t, db.Exec("CREATE FUNCTION "+schema+`.pg_advisory_xact_lock(bigint) RETURNS void LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_catalog.pg_advisory_xact_lock($1); INSERT INTO `+schema+`.children VALUES (1); END; $$`).Error)
	require.NoError(t, db.Exec("SET search_path TO "+schema+",pg_catalog").Error)
	dir := t.TempDir()
	path := filepath.Join(dir, "previous.bin")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0644))
	require.Error(t, plan.WriteFile(db, config.PlanConfig{Enabled: true, MaxStorageBytes: 100}, []string{dir}, path, []byte("replacement")))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
	require.Error(t, plan.WriteFile(db, config.PlanConfig{Enabled: true, MaxStorageBytes: 100}, []string{dir}, filepath.Join(dir, "new.bin"), []byte("new")))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "failed transactions leave no new or temporary files")
}
