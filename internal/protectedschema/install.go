package protectedschema

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/SMSFlow-ZA/whatomate-addons/notifications"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	extpostgres "github.com/shridarpatil/whatomate/pkg/extensions/postgres"
	"github.com/shridarpatil/whatomate/pkg/extensions/processingauth"
	extsecurity "github.com/shridarpatil/whatomate/pkg/extensions/security"
	"gorm.io/gorm"
)

var ErrCommitUnknown = errors.New("schema commit outcome unknown; keep isolated and inspect; do not retry")

// ValidateInstallation runs before reserving output or opening an owner DB.
func ValidateInstallation(c *config.Config, baseline bool) error {
	if c == nil || !c.Database.SchemaManagedExternally || !c.Extensions.ProtectedMode || c.Extensions.SyntheticDevelopment || c.Extensions.CarrierDispatchEnabled || c.Plan.Enabled || !releasePattern.MatchString(ReleaseIdentity) || c.Database.SchemaRelease != ReleaseIdentity {
		return ErrDenied
	}
	if !baseline {
		return Validate(c)
	}
	return nil
}

// Install is an explicit offline owner capability. Baseline is permitted only
// against an empty disposable DB and emits no seal. Target installation accepts
// only the compiled reviewed shape, never a caller-supplied captured baseline.
func Install(ctx context.Context, db *gorm.DB, c *config.Config, organization string, baseline bool) (Manifest, error) {
	if db == nil || ValidateInstallation(c, baseline) != nil {
		return Manifest{}, ErrDenied
	}
	if !baseline && Validate(c) != nil {
		return Manifest{}, ErrDenied
	}
	pool, e := db.DB()
	if e != nil {
		return Manifest{}, e
	}
	conn, e := pool.Conn(ctx)
	if e != nil {
		return Manifest{}, e
	}
	defer conn.Close()
	var held bool
	if e = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", LockID).Scan(&held); e != nil || !held {
		return Manifest{}, ErrDenied
	}
	defer func() {
		done, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.ExecContext(done, "SELECT pg_advisory_unlock($1)", LockID)
	}()
	var namespace, name, user string
	if e = conn.QueryRowContext(ctx, "SELECT current_schema(),current_database(),current_user").Scan(&namespace, &name, &user); e != nil || namespace != "public" || name != c.Database.Name || user != c.Database.User {
		return Manifest{}, ErrDenied
	}
	var extraNamespace bool
	if e = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE left(nspname,3)<>'pg_' AND nspname NOT IN('public','information_schema'))").Scan(&extraNamespace); e != nil || extraNamespace {
		return Manifest{}, ErrDenied
	}
	if baseline {
		m, e := Catalog(ctx, pool, namespace)
		if e != nil {
			return Manifest{}, e
		}
		for _, item := range m.Items {
			if !strings.HasPrefix(item, `["extension",`) && !strings.HasPrefix(item, `["postgres-major",`) {
				return Manifest{}, ErrDenied
			}
		}
	} else {
		id, e := uuid.Parse(organization)
		if e != nil || id == uuid.Nil || id.String() != organization {
			return Manifest{}, ErrDenied
		}
		protected, e := models.ProtectedHostEnabled(db)
		if e != nil || !protected {
			return Manifest{}, ErrDenied
		}
		var count int64
		if e = db.Model(&models.Organization{}).Where("id = ?", id).Count(&count).Error; e != nil || count != 1 {
			return Manifest{}, ErrDenied
		}
		if e = db.Model(&models.Organization{}).Count(&count).Error; e != nil || count != 1 {
			return Manifest{}, ErrDenied
		}
		if e = db.Model(&models.User{}).Where("email = ? AND organization_id = ? AND deleted_at IS NULL", c.DefaultAdmin.Email, id).Count(&count).Error; e != nil || count != 1 {
			return Manifest{}, ErrDenied
		}
	}
	key := ExpectedCatalogSHA256
	if baseline {
		key = "baseline"
	}
	var exists bool
	if e = conn.QueryRowContext(ctx, "SELECT to_regclass('whatomate_schema_attempt') IS NOT NULL").Scan(&exists); e != nil {
		return Manifest{}, e
	}
	if exists {
		var pending bool
		if e = conn.QueryRowContext(ctx, pendingSQL).Scan(&pending); e != nil || pending {
			return Manifest{}, ErrDenied
		}
		var attempted bool
		if e = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM whatomate_schema_attempt WHERE release=$1 AND catalog_sha256=$2)", ReleaseIdentity, key).Scan(&attempted); e != nil || attempted {
			return Manifest{}, ErrDenied
		}
	}
	tx, e := conn.BeginTx(ctx, nil)
	if e != nil {
		return Manifest{}, e
	}
	if _, e = tx.ExecContext(ctx, sealDDL); e == nil {
		_, e = tx.ExecContext(ctx, "INSERT INTO whatomate_schema_attempt(release,catalog_sha256) VALUES($1,$2)", ReleaseIdentity, key)
	}
	if e != nil {
		tx.Rollback()
		return Manifest{}, e
	}
	if e = tx.Commit(); e != nil {
		return Manifest{}, errors.Join(ErrCommitUnknown, e)
	}
	// Independently owned migration APIs use the pool. Cancel them if the exact
	// lock session is lost; the durable attempt additionally prevents admission
	// during cancellation or any unknown statement/commit outcome.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	defer func() { cancel(); <-stopped }()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, done := context.WithTimeout(ctx, time.Second)
				err := exclusiveHeld(check, conn)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	cfg := *c
	if baseline {
		random := make([]byte, 32)
		if _, e = rand.Read(random); e != nil {
			return Manifest{}, e
		}
		cfg.DefaultAdmin = config.DefaultAdminConfig{Email: "schema-reference@example.invalid", Password: hex.EncodeToString(random), FullName: "Isolated schema reference"}
	}
	if e = migrateOwner(ctx, db, &cfg); e != nil {
		return Manifest{}, e
	}
	// The same held physical session must survive all independently owned APIs.
	if e = exclusiveHeld(ctx, conn); e != nil {
		return Manifest{}, ErrDenied
	}
	manifest, e := Catalog(ctx, pool, namespace)
	if e != nil {
		return Manifest{}, e
	}
	if baseline {
		return manifest, nil
	}
	if manifest.SHA256 != ExpectedCatalogSHA256 {
		return Manifest{}, fmt.Errorf("%w: installed catalog differs from compiled expected shape", ErrDenied)
	}
	if _, e = conn.ExecContext(ctx, "INSERT INTO whatomate_schema_seal(release,catalog_sha256) VALUES($1,$2)", ReleaseIdentity, ExpectedCatalogSHA256); e != nil {
		return Manifest{}, errors.Join(ErrCommitUnknown, e)
	}
	return manifest, nil
}

// The pinned GORM migrator can panic while inspecting a cancelled PostgreSQL
// metadata query. Keep its durable attempt unsealed and return an opaque held
// result; never retry, emit internal values or let panic bypass CLI cleanup.
func migrateOwner(ctx context.Context, db *gorm.DB, c *config.Config) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrCommitUnknown
		}
	}()
	return Migrate(ctx, db, c)
}

// Migrate constructs only schema owners. It never creates an App, HTTP handler,
// Redis connection, model provider, carrier or background worker.
func Migrate(ctx context.Context, db *gorm.DB, c *config.Config) error {
	if e := db.WithContext(ctx).Exec(`CREATE TABLE IF NOT EXISTS extensions_bootstrap_attempt(id smallint PRIMARY KEY CHECK(id=1),complete boolean NOT NULL DEFAULT false)`).Error; e != nil {
		return e
	}
	if e := database.RunMigrationWithProgress(db.WithContext(ctx), &c.DefaultAdmin); e != nil {
		return e
	}
	// GORM's forward Message.DeliveryAttempts association materializes its FK
	// only once both sides exist. Complete that source-defined association in a
	// second successful pass; errors/unknown outcomes are never retried.
	if e := database.RunMigrationWithProgress(db.WithContext(ctx), &c.DefaultAdmin); e != nil {
		return e
	}
	if e := plan.InstallMediaSchema(db.WithContext(ctx)); e != nil {
		return e
	}
	if e := plan.Install(db.WithContext(ctx), c.Plan); e != nil {
		return e
	}
	pool, e := db.DB()
	if e != nil {
		return e
	}
	if e = processingauth.Migrate(ctx, pool); e != nil {
		return e
	}
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		return e
	}
	security, e := extsecurity.New(pool, key, extsecurity.Config{Drift: 1, EnrollmentTTL: 10 * time.Minute, MaxBreakGlass: time.Hour})
	if e != nil {
		return e
	}
	migrations := []func() error{
		func() error { return extpostgres.New(pool).Migrate(ctx) }, func() error { return security.Migrate(ctx) },
		func() error { return (addonapi.AccessStore{DB: pool}).Migrate(ctx) }, func() error { return directory.MigrateHTTP(ctx, pool) },
		func() error { return (&cases.SQLRepository{DB: pool}).Migrate(ctx) }, func() error { return knowledge.MigrateHTTP(ctx, pool) },
		func() error { return (ai.SQLStore{DB: pool}).Migrate(ctx) }, func() error { return conversation.Migrate(ctx, pool) },
		func() error { return (&cases.SQLRepository{DB: pool}).MigrateOutboxDelivery(ctx) }, func() error { return (reports.Store{DB: pool}).Migrate(ctx) },
		func() error { return carrier.Migrate(ctx, pool) }, func() error { return governance.MigrateHTTP(ctx, pool) },
		func() error { return notifications.Migrate(ctx, pool) },
	}
	for _, migrate := range migrations {
		if e = migrate(); e != nil {
			return e
		}
	}
	if c.Extensions.KnowledgeVectorEnabled {
		pin := knowledge.VectorPin{Model: c.Extensions.KnowledgeVectorIdentity, Dimensions: c.Extensions.AIEmbeddingDimensions}
		if pin.Model == "" || pin.Model == c.Extensions.AIEmbeddingModel || strings.TrimSpace(pin.Model) != pin.Model || len(pin.Model) > 256 {
			return ErrDenied
		}
		var organizations []models.Organization
		if e = db.Find(&organizations).Error; e != nil {
			return e
		}
		for _, org := range organizations {
			repo, e := knowledge.NewVectorRepository(pool, org.ID.String(), pin)
			if e != nil {
				return e
			}
			if e = repo.Migrate(ctx); e != nil {
				return e
			}
			if e = repo.RegisterIndexer(ctx, "knowledge-vector-indexer-v1"); e != nil {
				return e
			}
			if e = repo.Verify(ctx); e != nil {
				return e
			}
		}
	}
	if e = models.InstallProtectionGuards(db.WithContext(ctx)); e != nil {
		return e
	}
	return models.EnableProtectedHost(db.WithContext(ctx))
}

// Namespace is selected by the trusted connection search_path. Production
// deployment fixes it to public; isolated tests never touch that namespace.
func Namespace(ctx context.Context, db *sql.DB) (string, error) {
	var value string
	e := db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&value)
	if e != nil || !namespacePattern.MatchString(value) {
		return "", ErrDenied
	}
	return value, nil
}
