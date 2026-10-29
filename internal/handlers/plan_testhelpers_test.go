package handlers

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"testing"
)

func managedEntityApp(t *testing.T) (*App, *models.Organization, *models.User, *models.User) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	schema := "handler_plan_" + fmt.Sprintf("%x", uuid.New())
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
	require.NoError(t, db.AutoMigrate(&models.Organization{}, &models.Permission{}, &models.CustomRole{}, &models.User{}, &models.UserOrganization{}, &models.WhatsAppAccount{}, &models.AuditLog{}, &models.SSOProvider{}))
	redis := testutil.SetupTestRedis(t)
	if redis == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	app := &App{DB: db, Redis: redis, Log: testutil.NopLogger(), Config: &config.Config{App: config.AppConfig{EncryptionKey: "test-managed-plan-encryption-key-long-enough"}, Plan: config.PlanConfig{Enabled: true, MaxUsers: 1, MaxNumbers: 1}}}
	app.Config.Plan.MaxOrganizations = 1
	app.Config.JWT = config.JWTConfig{Secret: testutil.TestJWTSecret, AccessExpiryMins: 15, RefreshExpiryDays: 7}
	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, true, nil)
	owner := testutil.CreateTestUser(t, db, org.ID, testutil.WithEmail("owner@example.test"), testutil.WithSuperAdmin(), testutil.WithRoleID(&role.ID))
	require.NoError(t, plan.Install(db, app.Config.Plan))
	return app, org, owner, nil
}
