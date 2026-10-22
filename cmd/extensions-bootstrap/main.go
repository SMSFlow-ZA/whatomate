// extensions-bootstrap initializes a NEW database without starting a runtime.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/v2"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	extpostgres "github.com/shridarpatil/whatomate/pkg/extensions/postgres"
	extsecurity "github.com/shridarpatil/whatomate/pkg/extensions/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var errBootstrapBusy = errors.New("bootstrap database session lock unavailable")
var errRefused = errors.New("bootstrap refused; new empty database required; retain isolation and inspect privately")

const bootstrapLock int64 = 8331022201

type bootstrapResult struct {
	OrganizationID string `json:"organization_id"`
	AdminID        string `json:"admin_id"`
	Protected      bool   `json:"protected"`
	RuntimeReady   bool   `json:"runtime_ready"`
	MFAEnrolled    bool   `json:"mfa_enrolled"`
	DispatchHeld   bool   `json:"dispatch_held"`
}

// schema is an internal isolated-test seam. Production checks ALL user schemas.
// A session lock is deliberately held on one physical connection while owner
// migrations use the other pool connections and their own transactions.
func bootstrapDatabase(ctx context.Context, db *gorm.DB, schema, organization string, admin config.DefaultAdminConfig, afterCore func() error) (bootstrapResult, error) {
	var result bootstrapResult
	pool, e := db.DB()
	if e != nil {
		return result, e
	}
	conn, e := pool.Conn(ctx)
	if e != nil {
		return result, e
	}
	defer conn.Close()
	var locked bool
	if e = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", bootstrapLock).Scan(&locked); e != nil {
		return result, e
	}
	if !locked {
		return result, errBootstrapBusy
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlock, "SELECT pg_advisory_unlock($1)", bootstrapLock)
	}()
	if e = emptyDatabase(ctx, conn, schema); e != nil {
		return result, e
	}
	// Persist a sentinel BEFORE the core transaction. If migration rolls back or
	// the process dies, an incomplete database can never be mistaken for new.
	if _, e = conn.ExecContext(ctx, `CREATE TABLE extensions_bootstrap_attempt (id smallint PRIMARY KEY CHECK(id=1),complete boolean NOT NULL DEFAULT false);INSERT INTO extensions_bootstrap_attempt(id) VALUES(1)`); e != nil {
		return result, e
	}
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := database.RunMigrationWithProgress(tx, &admin); e != nil {
			return e
		}
		var org models.Organization
		if e := tx.First(&org).Error; e != nil {
			return e
		}
		if e := tx.Model(&org).Update("name", organization).Error; e != nil {
			return e
		}
		return nil
	})
	if e != nil {
		return result, e
	}
	if afterCore != nil {
		if e = afterCore(); e != nil {
			return result, e
		}
	}
	checkLock := func() error {
		var held bool
		// Query the original physical session, never another pool connection.
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND classid=($1::bigint >> 32)::oid AND objid=($1::bigint & 4294967295)::oid AND objsubid=1)`, bootstrapLock).Scan(&held); err != nil || !held {
			return errBootstrapBusy
		}
		return nil
	}
	if e = checkLock(); e != nil {
		return result, e
	}
	if e = migrateOwners(ctx, pool, checkLock); e != nil {
		return result, e
	}
	// Verify exact core identity and absence of fabricated access/MFA state before
	// installing the monotonic boundary. No login, session or grants are issued.
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkLock(); err != nil {
			return err
		}
		var org models.Organization
		var user models.User
		var n int64
		if e := tx.Model(&models.Organization{}).Count(&n).Error; e != nil || n != 1 {
			return errRefused
		}
		if e := tx.First(&org).Error; e != nil {
			return e
		}
		if e := tx.Model(&models.User{}).Count(&n).Error; e != nil || n != 1 {
			return errRefused
		}
		if e := tx.Where("email = ? AND organization_id = ? AND is_super_admin = true AND is_active = true", admin.Email, org.ID).First(&user).Error; e != nil {
			return e
		}
		if user.RoleID == nil || len(user.PasswordHash) == 0 {
			return errRefused
		}
		var memberships int64
		if e := tx.Model(&models.UserOrganization{}).Where("user_id = ? AND organization_id = ? AND role_id = ?", user.ID, org.ID, *user.RoleID).Count(&memberships).Error; e != nil || memberships != 1 {
			return errRefused
		}
		for _, table := range []string{"extension_security_mfa", "extension_security_recovery", "extension_security_breakglass", "addon_api_grants", "addon_api_identity_links", "addon_carrier_ingress", "addon_carrier_route", "addon_carrier_receipt", "addon_conversation_delivery"} {
			if e := tx.Table(table).Count(&n).Error; e != nil || n != 0 {
				return errRefused
			}
		}
		if e := models.InstallProtectionGuards(tx); e != nil {
			return e
		}
		if e := models.EnableProtectedHost(tx); e != nil {
			return e
		}
		if e := tx.Exec("UPDATE extensions_bootstrap_attempt SET complete=true WHERE id=1").Error; e != nil {
			return e
		}
		result = bootstrapResult{OrganizationID: org.ID.String(), AdminID: user.ID.String(), Protected: true, DispatchHeld: true}
		return nil
	})
	if e != nil {
		return bootstrapResult{}, e
	}
	var protected bool
	protected, e = models.ProtectedHostEnabled(db)
	if e != nil || !protected {
		return bootstrapResult{}, errRefused
	}
	return result, nil
}

func emptyDatabase(ctx context.Context, conn *sql.Conn, schema string) error {
	var count int64
	// Extension-owned objects (e.g. preinstalled pgvector) may exist. Any other
	// relation/routine/type in any user namespace refuses the production command.
	e := conn.QueryRowContext(ctx, `SELECT count(*) FROM (
 SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp_%' AND ($1='' OR n.nspname=$1)
 AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')
 UNION ALL SELECT p.oid FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_%' AND ($1='' OR n.nspname=$1)
 AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')
 UNION ALL SELECT t.oid FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_%' AND ($1='' OR n.nspname=$1)
 AND t.typtype IN ('d','e') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_type'::regclass AND d.objid=t.oid AND d.deptype='e')
 ) objects`, schema).Scan(&count)
	if e != nil {
		return e
	}
	if count != 0 {
		return errRefused
	}
	return nil
}

func migrateOwners(ctx context.Context, db *sql.DB, checkLock func() error) error {
	// Only documented schema ports. No App, provider, HTTP handler, transport,
	// Redis queue or indexer is constructed; no extension/user grants are seeded.
	// This ephemeral cipher is used only to construct the schema owner. It never
	// encrypts credentials, is not persisted, and is not the runtime security key.
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		return e
	}
	security, e := extsecurity.New(db, key, extsecurity.Config{Drift: 1, EnrollmentTTL: 10 * time.Minute, MaxBreakGlass: time.Hour})
	if e != nil {
		return e
	}
	migrations := []func() error{
		func() error { return extpostgres.New(db).Migrate(ctx) },
		func() error { return security.Migrate(ctx) },
		func() error { return (addonapi.AccessStore{DB: db}).Migrate(ctx) },
		func() error { return directory.MigrateHTTP(ctx, db) },
		func() error { return (&cases.SQLRepository{DB: db}).Migrate(ctx) },
		func() error { return knowledge.MigrateHTTP(ctx, db) },
		func() error { return (ai.SQLStore{DB: db}).Migrate(ctx) },
		func() error { return conversation.Migrate(ctx, db) },
		func() error { return (&cases.SQLRepository{DB: db}).MigrateOutboxDelivery(ctx) },
		func() error { return (reports.Store{DB: db}).Migrate(ctx) },
		func() error { return carrier.Migrate(ctx, db) },
		func() error { return governance.MigrateHTTP(ctx, db) },
	}
	for _, migrate := range migrations {
		if e := checkLock(); e != nil {
			return e
		}
		if e := migrate(); e != nil {
			return e
		}
	}
	return checkLock()
}

func validateBootstrapConfig(c *config.Config, databaseName, organization string) error {
	if c == nil || !regexp.MustCompile(`^[a-z][a-z0-9_]{2,62}$`).MatchString(databaseName) || c.Database.Name != databaseName || c.Database.Host == "" || c.Database.User == "" || c.Database.Password == "" || c.Database.Port < 1 || c.Database.Port > 65535 || c.Database.SSLMode != "verify-full" {
		return errRefused
	}
	if c.App.Environment != "production" || c.App.Debug || !c.Extensions.ProtectedMode || c.Extensions.SyntheticDevelopment || c.Extensions.CarrierEnabled || c.Extensions.CarrierDispatchEnabled || len(c.Extensions.CarrierBindings) > 0 || len(c.Extensions.CarrierSMSBindings) > 0 || !c.Channels.StopDispatch {
		return errRefused
	}
	a, e := mail.ParseAddress(c.DefaultAdmin.Email)
	if e != nil || a.Address != c.DefaultAdmin.Email || len(c.DefaultAdmin.Password) < 20 || len(c.DefaultAdmin.Password) > 72 || strings.TrimSpace(c.DefaultAdmin.FullName) == "" || strings.TrimSpace(organization) == "" || len(organization) > 100 {
		return errRefused
	}
	return nil
}

func readConfig(path string) (*config.Config, error) {
	if path == "" || os.Geteuid() != 0 {
		return nil, errRefused
	}
	before, e := os.Lstat(path)
	if e != nil {
		return nil, errRefused
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || !rootOwned(before) || before.Size() > 1<<20 {
		return nil, errRefused
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errRefused
	}
	defer f.Close()
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		return nil, errRefused
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return nil, errRefused
	}
	// Deliberately no environment provider and no default credentials/defaults.
	k := koanf.New(".")
	if e = k.Load(rawConfig(b), toml.Parser()); e != nil {
		return nil, errRefused
	}
	var cfg config.Config
	if e = k.Unmarshal("", &cfg); e != nil {
		return nil, errRefused
	}
	return &cfg, nil
}

type rawConfig []byte

func (r rawConfig) ReadBytes() ([]byte, error)            { return r, nil }
func (r rawConfig) Read() (map[string]interface{}, error) { return nil, errors.New("bytes only") }

func run(args []string, out io.Writer) error {
	f := flag.NewFlagSet("extensions-bootstrap", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := f.String("config", "", "root-owned private TOML config")
	name := f.String("database", "", "exact NEW database name")
	org := f.String("organization-name", "", "initial organization display name")
	if e := f.Parse(args); e != nil || f.NArg() != 0 {
		return errRefused
	}
	c, e := readConfig(*path)
	if e != nil {
		return errRefused
	}
	if e = validateBootstrapConfig(c, *name, *org); e != nil {
		return e
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.Database.User, c.Database.Password), Host: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Path: "/" + c.Database.Name}
	q := url.Values{"sslmode": []string{c.Database.SSLMode}, "search_path": []string{"public"}, "connect_timeout": []string{"10"}}
	u.RawQuery = q.Encode()
	// The only external I/O is this explicitly configured PostgreSQL connection.
	db, e := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		return errRefused
	}
	pool, e := db.DB()
	if e != nil {
		return errRefused
	}
	defer pool.Close()
	pool.SetMaxOpenConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var actual string
	if e = pool.QueryRowContext(ctx, "SELECT current_database()").Scan(&actual); e != nil || actual != *name {
		return errRefused
	}
	result, e := bootstrapDatabase(ctx, db, "", *org, c.DefaultAdmin, nil)
	if e != nil {
		return errRefused
	}
	return json.NewEncoder(out).Encode(result)
}
func main() {
	if e := run(os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "extensions-bootstrap: refused; keep database isolated; no automatic retry or deletion")
		os.Exit(1)
	}
}
