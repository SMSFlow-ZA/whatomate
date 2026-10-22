package handlers

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestExternalSchemaFullProtectedInitializationDMLOnly(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("real local PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Hostname() != "db" || u.Path != "/hosttests" || u.User.Username() != "addon_test" {
		t.Fatal("refuse nonlocal/shared target")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	name := "schema_handler_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	role := "schema_hr_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, e = admin.Exec("CREATE DATABASE " + name); e != nil {
		t.Fatal(e)
	}
	var pools []*sql.DB
	t.Cleanup(func() {
		for _, p := range pools {
			p.Close()
		}
		if !regexp.MustCompile(`^schema_handler_test_[a-f0-9]{24}$`).MatchString(name) {
			t.Error("unsafe cleanup")
			return
		}
		if _, e := admin.Exec("DROP DATABASE " + name); e != nil {
			t.Error(e)
		}
		admin.Exec("DROP ROLE IF EXISTS " + role)
		admin.Close()
	})
	local := *u
	local.Path = "/" + name
	owner, e := gorm.Open(postgres.Open(local.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, e := owner.DB()
	if e != nil {
		t.Fatal(e)
	}
	pools = append(pools, pool)
	pool.SetMaxOpenConns(5)
	oldR, oldS := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity = oldR; protectedschema.ExpectedCatalogSHA256 = oldS })
	protectedschema.ReleaseIdentity = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	protectedschema.ExpectedCatalogSHA256 = ""
	c := &config.Config{}
	c.Database.Name = name
	c.Database.User = u.User.Username()
	c.Database.SchemaManagedExternally = true
	c.Database.SchemaRelease = protectedschema.ReleaseIdentity
	c.Extensions = validProductionAIHostConfig()
	c.Extensions.ProtectedMode = true
	c.Extensions.SecurityKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	c.Extensions.MFAFreshnessSeconds = 900
	c.Extensions.Modules = "directory,cases,knowledge,ai,conversation,reports,governance"
	c.Extensions.RosterFreshnessSeconds = 86400
	c.Extensions.EscalationAfterSeconds = 3600
	c.Extensions.ConversationEmployeeAudience = "employees"
	c.Extensions.ReportsMinGroup = 5
	ctx := context.Background()
	manifest, e := protectedschema.Install(ctx, owner, c, "", true)
	if e != nil {
		t.Fatal(e)
	}
	protectedschema.ExpectedCatalogSHA256 = manifest.SHA256
	c.Database.SchemaSHA256 = manifest.SHA256
	c.DefaultAdmin.Email = "schema-reference@example.invalid"
	var org models.Organization
	if e = owner.First(&org).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = protectedschema.Install(ctx, owner, c, org.ID.String(), false); e != nil {
		t.Fatal(e)
	}
	password := strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec("CREATE ROLE " + role + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + password + "'"); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("REVOKE ALL ON DATABASE " + name + " FROM PUBLIC;GRANT CONNECT ON DATABASE " + name + " TO " + role); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec("REVOKE ALL ON SCHEMA public FROM PUBLIC;GRANT USAGE ON SCHEMA public TO " + role + ";GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + role + ";GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO " + role); e != nil {
		t.Fatal(e)
	}
	// Permit business DML, never immutable release/protection/migration policy.
	rows, e := pool.Query(`SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('whatomate_schema_seal','whatomate_schema_attempt','extensions_bootstrap_attempt','whatomate_protection_state','extension_schema','extension_security_schema','addon_cases_schema','addon_knowledge_vector_profiles','addon_knowledge_vector_workers','er_processing_authorization_bindings')`)
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var table string
		if e = rows.Scan(&table); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, table)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	for _, table := range tables {
		if _, e = pool.Exec("GRANT INSERT,UPDATE,DELETE ON " + table + " TO " + role); e != nil {
			t.Fatal(e)
		}
	}
	local.User = url.UserPassword(role, password)
	c.Database.User = role
	c.Database.Password = password
	c.Database.Host = u.Hostname()
	c.Database.Port, _ = strconv.Atoi(u.Port())
	c.Database.SSLMode = "disable"
	runtime, e := protectedschema.OpenRuntime(c)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := runtime.DB()
	if e != nil {
		t.Fatal(e)
	}
	pools = append(pools, raw)
	raw.SetMaxOpenConns(5)
	c.Database.User = role
	a := &App{DB: runtime, Config: c}
	if e = a.InitializeExtensions(); e != nil {
		t.Fatalf("DML-only full initialization: %v", e)
	}
	if a.ExtensionSecurity == nil || a.ExtensionConversation == nil || a.ExtensionAccess == nil || len(a.ExtensionAPI) != 7 {
		t.Fatal("composition incomplete")
	}
	// This exercises the real Connection/NewDB storage callback, not only a direct setting read.
	if e = plan.RecoverMediaPublications(a.DB, []string{t.TempDir()}); e != nil {
		t.Fatalf("DML-only media recovery: %v", e)
	}
	if _, e = raw.Exec("CREATE TABLE forbidden(id integer)"); e == nil {
		t.Fatal("runtime can create table")
	}
	if _, e = pool.Exec("ALTER TABLE addon_cases_case ADD COLUMN corrupt text"); e != nil {
		t.Fatal(e)
	}
	refused := &App{DB: runtime, Config: c}
	if e = refused.InitializeExtensions(); e == nil || refused.ExtensionRegistry != nil || refused.ExtensionSecurity != nil {
		t.Fatal("tampered catalog reached module registration")
	}
}

func TestSchemaMigrationOrdinaryCompatibilityAndExternalClosedDefault(t *testing.T) {
	calls := 0
	a := &App{Config: &config.Config{}}
	if e := a.extensionSchemaMigration(context.Background(), nil, func() error { calls++; return nil }); e != nil || calls != 1 {
		t.Fatal("ordinary migration changed", e)
	}
	a.Config.Database.SchemaManagedExternally = true
	if e := a.extensionSchemaMigration(context.Background(), nil, func() error { calls++; return nil }); e == nil || calls != 1 {
		t.Fatal("unverified external mode migrated")
	}
}
