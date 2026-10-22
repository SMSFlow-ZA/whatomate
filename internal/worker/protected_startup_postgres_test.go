package worker

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func workerStartupDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("worker_startup_%d", time.Now().UnixNano())
	if _, err = base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	if err = models.InstallProtectedHostGuard(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestStandaloneWorkerRefusesPersistedProtectedHostBeforeRedis(t *testing.T) {
	db := workerStartupDatabase(t)
	if err := models.EnableProtectedHost(db); err != nil {
		t.Fatal(err)
	}
	// Constructor must reject the persistent boundary with omitted module flags.
	// A nil Redis handle proves no queue/group side effect precedes refusal.
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("protected worker reached Redis before persistent guard: %v", recovered)
		}
	}()
	w, err := New(&config.Config{}, db, nil, testutil.NopLogger())
	if err == nil || w != nil || !strings.Contains(err.Error(), "persisted protection") {
		t.Fatalf("ordinary worker accepted restored ER host: %v", err)
	}
}

func TestStandaloneWorkerDatabaseBoundaryOrdinaryAndMissing(t *testing.T) {
	db := workerStartupDatabase(t)
	if err := validateWorkerDatabase(db); err != nil {
		t.Fatalf("ordinary migrated database refused: %v", err)
	}
	if err := db.Exec(`DROP TABLE whatomate_protection_state`).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("missing marker reached Redis: %v", recovered)
		}
	}()
	w, err := New(&config.Config{}, db, nil, testutil.NopLogger())
	if err == nil || w != nil || !strings.Contains(err.Error(), "persisted protection") {
		t.Fatalf("missing marker did not fail closed: %v", err)
	}
}
