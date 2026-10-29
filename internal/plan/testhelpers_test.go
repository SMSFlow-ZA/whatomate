package plan

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"testing"
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
 CREATE TABLE organizations(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text, deleted_at timestamptz);
 CREATE TABLE whatsapp_accounts(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid, name text, status text, deleted_at timestamptz);
 CREATE TABLE user_organizations(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid, organization_id uuid, role_id uuid, is_default bool, deleted_at timestamptz, updated_at timestamptz);
 CREATE TABLE custom_roles(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid, name text, is_system bool, deleted_at timestamptz);`).Error)
	return db
}

func quotaConfig(users, numbers int) config.PlanConfig {
	return config.PlanConfig{Enabled: true, MaxUsers: users, MaxNumbers: numbers}
}
