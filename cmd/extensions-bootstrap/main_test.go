package main

import (
	"context"
	"database/sql"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDatabase(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	dsn := os.Getenv("BOOTSTRAP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BOOTSTRAP_TEST_DATABASE_URL required; real PostgreSQL only")
	}
	base, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "bootstrap_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = base.Exec("CREATE SCHEMA " + schema); e != nil {
		t.Fatal(e)
	}
	// Each pool's startup search_path is fixed; no other schema is touched.
	db, e := gorm.Open(postgres.Open(dsn+"&search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	pool.SetMaxOpenConns(5)
	t.Cleanup(func() { pool.Close(); base.Exec("DROP SCHEMA " + schema + " CASCADE"); base.Close() })
	return db, schema
}
func adminConfig() config.DefaultAdminConfig {
	return config.DefaultAdminConfig{Email: "bootstrap@example.invalid", Password: "synthetic-bootstrap-only-password", FullName: "Bootstrap operator"}
}

func TestBootstrapFreshAndRefusesReplay(t *testing.T) {
	db, schema := testDatabase(t)
	result, e := bootstrapDatabase(context.Background(), db, schema, "Synthetic organization", adminConfig(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if result.OrganizationID == "" || result.AdminID == "" {
		t.Fatal("missing verified identity")
	}
	protected, e := models.ProtectedHostEnabled(db)
	if e != nil || !protected {
		t.Fatalf("marker: %v %v", protected, e)
	}
	for _, table := range []string{"extension_security_mfa", "extension_security_recovery", "extension_security_breakglass", "addon_api_grants", "addon_carrier_ingress", "addon_conversation_delivery"} {
		var exists bool
		db.Raw("SELECT to_regclass(?) IS NOT NULL", schema+"."+table).Scan(&exists)
		if !exists {
			t.Fatalf("owner schema missing: %s", table)
		}
		if exists {
			var count int64
			if e := db.Table(table).Count(&count).Error; e != nil || count != 0 {
				t.Fatalf("fabricated state %s: %d %v", table, count, e)
			}
		}
	}
	if _, e = bootstrapDatabase(context.Background(), db, schema, "Other", adminConfig(), nil); e == nil {
		t.Fatal("protected replay accepted")
	}
}
func TestBootstrapRejectsNonemptyAndPartial(t *testing.T) {
	t.Run("nonempty", func(t *testing.T) {
		db, s := testDatabase(t)
		db.Exec("CREATE TABLE existing_business_data(id integer)")
		if _, e := bootstrapDatabase(context.Background(), db, s, "Synthetic", adminConfig(), nil); e == nil {
			t.Fatal("existing DB accepted")
		}
		var exists bool
		db.Raw("SELECT to_regclass('users') IS NOT NULL").Scan(&exists)
		if exists {
			t.Fatal("refusal mutated schema")
		}
	})
	t.Run("partial", func(t *testing.T) {
		db, s := testDatabase(t)
		_, e := bootstrapDatabase(context.Background(), db, s, "Synthetic", adminConfig(), func() error { return errors.New("injected owner failure") })
		if e == nil {
			t.Fatal("failure accepted")
		}
		if _, e = bootstrapDatabase(context.Background(), db, s, "Synthetic", adminConfig(), nil); e == nil {
			t.Fatal("partial DB retried")
		}
		var exists bool
		db.Raw("SELECT to_regclass('whatomate_protection_state') IS NOT NULL").Scan(&exists)
		if exists {
			t.Fatal("partial bootstrap marked ready")
		}
	})
}
func TestBootstrapConcurrentSessionRefuses(t *testing.T) {
	db, s := testDatabase(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := bootstrapDatabase(context.Background(), db, s, "Synthetic", adminConfig(), func() error { close(entered); <-release; return errors.New("stop") })
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(time.Minute):
		t.Fatal("first initializer stalled")
	}
	if _, e := bootstrapDatabase(context.Background(), db, s, "Synthetic", adminConfig(), nil); !errors.Is(e, errBootstrapBusy) {
		close(release)
		<-done
		t.Fatalf("concurrent bootstrap not refused by physical session lock: %v", e)
	}
	close(release)
	if e := <-done; e == nil {
		t.Fatal("injected first failure accepted")
	}
}

func TestCoreFailureRetainsAttemptAndRollsBackIdentity(t *testing.T) {
	db, s := testDatabase(t)
	admin := adminConfig()
	admin.Password = strings.Repeat("x", 73)
	if _, e := bootstrapDatabase(context.Background(), db, s, "Synthetic", admin, nil); e == nil {
		t.Fatal("bcrypt failure accepted")
	}
	var count int
	if e := db.Raw("SELECT count(*) FROM extensions_bootstrap_attempt WHERE NOT complete").Scan(&count).Error; e != nil || count != 1 {
		t.Fatalf("attempt sentinel not retained: %v", e)
	}
	var exists bool
	db.Raw("SELECT to_regclass('organizations') IS NOT NULL").Scan(&exists)
	if exists {
		t.Fatal("core schema did not roll back")
	}
	if _, e := bootstrapDatabase(context.Background(), db, s, "Synthetic", adminConfig(), nil); e == nil {
		t.Fatal("failed attempt retried")
	}
}

func TestNoRuntimeDependencies(t *testing.T) {
	file, e := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if e != nil {
		t.Fatal(e)
	}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		for _, forbidden := range []string{"net/http", "redis", "/handlers", "/worker", "/channels", "/provider"} {
			if strings.Contains(path, forbidden) {
				t.Fatalf("runtime dependency added: %s", path)
			}
		}
	}
}

func TestPrivateFileAndNoEnvironmentOverride(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned Linux deployment file boundary")
	}
	path := filepath.Join(t.TempDir(), "bootstrap.toml")
	if e := os.WriteFile(path, []byte("[database]\nname='trusted_file_database'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("WHATOMATE_DATABASE__NAME", "attacker_environment")
	c, e := readConfig(path)
	if e != nil || c.Database.Name != "trusted_file_database" {
		t.Fatalf("file config overridden: %v", e)
	}
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = readConfig(path); e == nil {
		t.Fatal("readable secret config accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(filepath.Dir(path), "link.toml")
	if e = os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e = readConfig(link); e == nil {
		t.Fatal("symlink config accepted")
	}
}

func TestValidateBootstrapHolds(t *testing.T) {
	cfg := &config.Config{DefaultAdmin: adminConfig()}
	cfg.Database.Name = "isolated_new_db"
	cfg.Database.Host = "db"
	cfg.Database.Port = 5432
	cfg.Database.User = "bootstrap"
	cfg.Database.Password = "synthetic"
	cfg.Database.SSLMode = "verify-full"
	cfg.App.Environment = "production"
	cfg.Extensions.ProtectedMode = true
	cfg.Channels.StopDispatch = true
	if e := validateBootstrapConfig(cfg, "isolated_new_db", "Synthetic"); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*config.Config){func(c *config.Config) { c.Extensions.ProtectedMode = false }, func(c *config.Config) { c.Extensions.CarrierEnabled = true }, func(c *config.Config) { c.Extensions.CarrierDispatchEnabled = true }, func(c *config.Config) { c.Channels.StopDispatch = false }, func(c *config.Config) { c.Extensions.SyntheticDevelopment = true }, func(c *config.Config) { c.Database.Name = "other" }} {
		copy := *cfg
		mutate(&copy)
		if validateBootstrapConfig(&copy, "isolated_new_db", "Synthetic") == nil {
			t.Fatal("unsafe bootstrap config accepted")
		}
	}
}
