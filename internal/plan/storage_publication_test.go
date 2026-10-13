package plan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStorageUsesPersistedAllowanceAfterWorkerConfigBecomesStale(t *testing.T) {
	db := quotaDB(t)
	old := quotaConfig(2, 1)
	old.MaxStorageBytes = 20
	require.NoError(t, Install(db, old))
	current := old
	current.MaxStorageBytes = 3
	require.NoError(t, Install(db, current))
	dir := t.TempDir()
	require.ErrorIs(t, WriteFile(db, old, []string{dir}, filepath.Join(dir, "large.bin"), []byte("1234")), ErrStorageLimit)
	effective, err := EffectiveLimits(db, old)
	require.NoError(t, err)
	require.EqualValues(t, 3, effective.MaxStorageBytes)
	require.Error(t, WriteFile(db, config.PlanConfig{}, []string{dir}, filepath.Join(dir, "disabled.bin"), []byte("x")))
}

func TestStoragePublicationFailureRestoresFileAndMetadata(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(1, 1)
	cfg.MaxStorageBytes = 3
	require.NoError(t, Install(db, cfg))
	require.NoError(t, db.Exec("CREATE TABLE media_metadata(value text); INSERT INTO media_metadata VALUES ('old')").Error)
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.bin")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0644))
	err := WriteFileWithCommit(db, cfg, []string{dir}, path, []byte("new"), func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("UPDATE media_metadata SET value='new'").Error)
		return errors.New("provider rejected publication")
	})
	require.ErrorContains(t, err, "provider rejected")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	var value string
	require.NoError(t, db.Raw("SELECT value FROM media_metadata").Scan(&value).Error)
	require.Equal(t, "old", value)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestStorageStatusWaitsForPublicationAndCleanup(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(1, 1)
	cfg.MaxStorageBytes = 10
	require.NoError(t, Install(db, cfg))
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.bin")
	require.NoError(t, os.WriteFile(path, []byte("12345678"), 0644))
	reached := make(chan struct{})
	release := make(chan struct{})
	written := make(chan error, 1)
	go func() {
		written <- WriteFileWithCommit(db, cfg, []string{dir}, path, []byte("1234567890"), func(*gorm.DB) error { close(reached); <-release; return nil })
	}()
	<-reached
	observed := make(chan int64, 1)
	scanErr := make(chan error, 1)
	go func() {
		_, _, _, bytes, err := UsageSnapshot(db, cfg, []string{dir})
		observed <- bytes
		scanErr <- err
	}()
	select {
	case bytes := <-observed:
		close(release)
		<-written
		t.Fatalf("status observed in-progress overwrite: %d bytes", bytes)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-written)
	require.EqualValues(t, 10, <-observed)
	require.NoError(t, <-scanErr)
}

func TestStorageFailedPublicationCannotRollBackAConcurrentWinner(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(1, 1)
	cfg.MaxStorageBytes = 3
	require.NoError(t, Install(db, cfg))
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.bin")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0644))
	reached := make(chan struct{})
	release := make(chan struct{})
	failed := make(chan error, 1)
	winner := make(chan error, 1)
	go func() {
		failed <- WriteFileWithCommit(db, cfg, []string{dir}, path, []byte("bad"), func(*gorm.DB) error { close(reached); <-release; return errors.New("provider rejected") })
	}()
	<-reached
	go func() { winner <- WriteFile(db, cfg, []string{dir}, path, []byte("win")) }()
	select {
	case err := <-winner:
		close(release)
		<-failed
		t.Fatalf("writer passed unresolved publication: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.ErrorContains(t, <-failed, "provider rejected")
	require.NoError(t, <-winner)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "win", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestStoragePlanInstallationWaitsForPublication(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(1, 1)
	cfg.MaxStorageBytes = 10
	require.NoError(t, Install(db, cfg))
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.bin")
	reached := make(chan struct{})
	release := make(chan struct{})
	written := make(chan error, 1)
	installed := make(chan error, 1)
	go func() {
		written <- WriteFileWithCommit(db, cfg, []string{dir}, path, []byte("12345"), func(*gorm.DB) error { close(reached); <-release; return nil })
	}()
	<-reached
	lower := cfg
	lower.MaxStorageBytes = 3
	go func() { installed <- Install(db, lower) }()
	select {
	case err := <-installed:
		close(release)
		<-written
		t.Fatalf("plan changed during publication: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-written)
	require.NoError(t, <-installed)
	require.ErrorIs(t, WriteFile(db, cfg, []string{dir}, filepath.Join(dir, "additional.bin"), []byte("x")), ErrStorageLimit)
}
