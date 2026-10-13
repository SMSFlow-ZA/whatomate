package plan

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Each case owns a schema: managed triggers must never affect upstream fixtures.
func quotaDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	schema := "plan_test_" + fmt.Sprintf("%x", uuid.New())
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		sql, _ := admin.DB()
		_ = sql.Close()
	})
	if parsed, e := url.Parse(dsn); e == nil && parsed.Scheme != "" {
		q := parsed.Query()
		q.Set("search_path", schema)
		parsed.RawQuery = q.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() { sql, _ := db.DB(); _ = sql.Close() })
	require.NoError(t, db.Exec(`CREATE TABLE users(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text UNIQUE NOT NULL, organization_id uuid, password_hash text, full_name text, role_id uuid, is_active bool DEFAULT true, is_super_admin bool DEFAULT false, sso_provider text, sso_provider_id text, settings jsonb, deleted_at timestamptz, updated_at timestamptz);
 CREATE TABLE whatsapp_accounts(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid, name text, status text, deleted_at timestamptz);
 CREATE TABLE user_organizations(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid, organization_id uuid, role_id uuid, is_default bool, deleted_at timestamptz, updated_at timestamptz);
 CREATE TABLE custom_roles(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid, name text, is_system bool, deleted_at timestamptz);`).Error)
	require.NoError(t, db.Exec("INSERT INTO users(email,is_super_admin,password_hash) VALUES ('support@example.test',true,'host-hash')").Error)
	return db
}

func TestEnsureSupportBootstrapsOnceAndPreservesPassword(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, db.Exec("DELETE FROM users").Error)
	org := uuid.New()
	require.NoError(t, db.Exec("INSERT INTO users(email,organization_id) VALUES ('owner@example.test',?)", org).Error)
	require.NoError(t, db.Exec("INSERT INTO custom_roles(organization_id,name,is_system) VALUES (?,'admin',true)", org).Error)
	cfg := quotaConfig(1, 1)
	cfg.SupportPassword = "bootstrap-test-password"
	require.NoError(t, EnsureSupport(db, cfg, "owner@example.test"))
	var hash string
	require.NoError(t, db.Raw("SELECT password_hash FROM users WHERE email=?", cfg.SupportEmail).Scan(&hash).Error)
	require.NotEmpty(t, hash)
	require.NotEqual(t, cfg.SupportPassword, hash)
	cfg.SupportPassword = "another-bootstrap-password"
	require.NoError(t, EnsureSupport(db, cfg, "owner@example.test"))
	var unchanged string
	require.NoError(t, db.Raw("SELECT password_hash FROM users WHERE email=?", cfg.SupportEmail).Scan(&unchanged).Error)
	require.Equal(t, hash, unchanged)
	require.NoError(t, Install(db, cfg))
}

func TestEnsureSupportFailsClosedWithoutSafeBootstrap(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, db.Exec("DELETE FROM users").Error)
	cfg := quotaConfig(1, 1)
	cfg.SupportPassword = "short"
	require.Error(t, EnsureSupport(db, cfg, "owner@example.test"))
	cfg.SupportPassword = "bootstrap-test-password"
	require.Error(t, EnsureSupport(db, cfg, "owner@example.test"))
	require.Error(t, Install(db, cfg))
}

func quotaConfig(users, numbers int) config.PlanConfig {
	return config.PlanConfig{Enabled: true, MaxUsers: users, MaxNumbers: numbers, MaxStorageBytes: 1024, SupportEmail: "support@example.test", SourceURL: "https://example.test/source"}
}

func TestClassifyOnlyStablePlanConstraints(t *testing.T) {
	for _, tc := range []struct{ name, resource string }{{"whatomate_plan_users_limit", "users"}, {"whatomate_plan_numbers_limit", "numbers"}} {
		resource, ok := Classify(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23514", ConstraintName: tc.name}))
		require.True(t, ok)
		require.Equal(t, tc.resource, resource)
	}
	for _, err := range []error{nil, errors.New("whatomate_plan_users_limit"), &pgconn.PgError{Code: "23505", ConstraintName: "whatomate_plan_users_limit"}} {
		_, ok := Classify(err)
		require.False(t, ok)
	}
}

func TestDatabaseGlobalUserLimitDeletionRestoreAndLowering(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(2, 1)
	require.NoError(t, Install(db, cfg))
	require.NoError(t, db.Exec("INSERT INTO users(email,organization_id,is_active) VALUES ('owner@example.test',?,false),('other@example.test',?,true)", uuid.New(), uuid.New()).Error)
	err := db.Exec("INSERT INTO users(email,is_super_admin) VALUES ('third@example.test',true)").Error
	resource, ok := Classify(err)
	require.True(t, ok)
	require.Equal(t, "users", resource)
	require.NoError(t, db.Exec("UPDATE users SET deleted_at=now() WHERE email='other@example.test'").Error)
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('third@example.test')").Error)
	_, ok = Classify(db.Exec("UPDATE users SET deleted_at=NULL WHERE email='other@example.test'").Error)
	require.True(t, ok)
	cfg.MaxUsers = 1
	require.NoError(t, Install(db, cfg))
	require.NoError(t, db.Exec("UPDATE users SET full_name='still editable' WHERE email='owner@example.test'").Error)
	users, numbers, err := Usage(db, cfg)
	require.NoError(t, err)
	require.EqualValues(t, 2, users)
	require.EqualValues(t, 0, numbers)
	require.Error(t, Install(db, config.PlanConfig{}))
}

func TestDatabaseNumbersBatchPendingAndRestore(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(2, 1)
	require.NoError(t, Install(db, cfg))
	require.Error(t, db.Exec("INSERT INTO whatsapp_accounts(name,status) VALUES ('one','pending'),('two','inactive')").Error)
	_, numbers, err := Usage(db, cfg)
	require.NoError(t, err)
	require.Zero(t, numbers)
	require.NoError(t, db.Exec("INSERT INTO whatsapp_accounts(name,status) VALUES ('one','pending')").Error)
	resource, ok := Classify(db.Exec("INSERT INTO whatsapp_accounts(name,status) VALUES ('two','inactive')").Error)
	require.True(t, ok)
	require.Equal(t, "numbers", resource)
	require.NoError(t, db.Exec("UPDATE whatsapp_accounts SET deleted_at=now() WHERE name='one'").Error)
	require.NoError(t, db.Exec("INSERT INTO whatsapp_accounts(name) VALUES ('two')").Error)
	_, ok = Classify(db.Exec("UPDATE whatsapp_accounts SET deleted_at=NULL WHERE name='one'").Error)
	require.True(t, ok)
}

func TestDatabaseConcurrentFinalSlot(t *testing.T) {
	for _, table := range []string{"users", "whatsapp_accounts"} {
		t.Run(table, func(t *testing.T) {
			db := quotaDB(t)
			require.NoError(t, Install(db, quotaConfig(1, 1)))
			var wg sync.WaitGroup
			errs := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					query := "INSERT INTO users(email) VALUES (?)"
					if table == "whatsapp_accounts" {
						query = "INSERT INTO whatsapp_accounts(name) VALUES (?)"
					}
					errs <- db.Exec(query, fmt.Sprintf("candidate%d@example.test", i)).Error
				}(i)
			}
			wg.Wait()
			close(errs)
			successes := 0
			for err := range errs {
				if err == nil {
					successes++
				} else {
					_, ok := Classify(err)
					require.True(t, ok, "%v", err)
				}
			}
			require.Equal(t, 1, successes)
		})
	}
}

func TestDatabaseProtectsSupportIdentityAndMembership(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, db.Exec("INSERT INTO user_organizations(user_id,organization_id) SELECT id,? FROM users", uuid.New()).Error)
	require.NoError(t, Install(db, quotaConfig(1, 1)))
	for _, query := range []string{
		"UPDATE users SET email='customer@example.test'", "UPDATE users SET password_hash='changed'", "UPDATE users SET is_super_admin=false", "UPDATE users SET is_active=false", "UPDATE users SET role_id=gen_random_uuid()", "UPDATE users SET sso_provider='custom'", "UPDATE users SET deleted_at=now()", "DELETE FROM users", "DELETE FROM user_organizations", "UPDATE user_organizations SET role_id=gen_random_uuid()", "INSERT INTO users(email) VALUES ('SUPPORT@example.test')",
	} {
		require.True(t, IsSupportProtected(db.Exec(query).Error), query)
	}
	require.NoError(t, db.Exec("UPDATE users SET updated_at=now(),settings='{}'").Error)
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('customer@example.test')").Error)
	users, _, err := Usage(db, quotaConfig(1, 1))
	require.NoError(t, err)
	require.EqualValues(t, 1, users)
	cfg := quotaConfig(1, 1)
	cfg.SupportEmail = "customer@example.test"
	require.Error(t, Install(db, cfg))
}

func TestDatabaseIgnoredUpsertAndRollbackDoNotConsumeSeats(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(2, 1)
	require.NoError(t, Install(db, cfg))
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('one@example.test')").Error)
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('one@example.test') ON CONFLICT(email) DO NOTHING").Error)
	tx := db.Begin()
	require.NoError(t, tx.Exec("INSERT INTO users(email) VALUES ('rolledback@example.test')").Error)
	require.NoError(t, tx.Rollback().Error)
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('two@example.test')").Error)
	_, ok := Classify(db.Exec("INSERT INTO users(email) VALUES ('three@example.test')").Error)
	require.True(t, ok)
	users, _, err := Usage(db, cfg)
	require.NoError(t, err)
	require.EqualValues(t, 2, users)
}

func TestDatabaseRepeatableReadCannotUseStaleSeatCount(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(1, 1)
	require.NoError(t, Install(db, cfg))
	stale := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	require.NoError(t, stale.Error)
	defer stale.Rollback()
	var count int64
	require.NoError(t, stale.Raw("SELECT count(*) FROM users").Scan(&count).Error)
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('winner@example.test')").Error)
	require.Error(t, stale.Exec("INSERT INTO users(email) VALUES ('stale@example.test')").Error)
	require.NoError(t, stale.Rollback().Error)
	users, _, err := Usage(db, cfg)
	require.NoError(t, err)
	require.EqualValues(t, 1, users)
}

func TestDatabaseInstallRejectsAmbiguousSupportIdentity(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('SUPPORT@example.test')").Error)
	require.Error(t, Install(db, quotaConfig(1, 1)))
}

func TestDatabaseUnmanagedInstallPreservesUpstreamBehavior(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, Install(db, config.PlanConfig{}))
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('one@example.test'),('two@example.test')").Error)
	users, _, err := Usage(db, config.PlanConfig{})
	require.NoError(t, err)
	require.EqualValues(t, 3, users)
}
