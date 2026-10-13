package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
		sql.Close()
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
	t.Cleanup(func() { sql, _ := db.DB(); sql.Close() })
	require.NoError(t, db.AutoMigrate(&models.Organization{}, &models.Permission{}, &models.CustomRole{}, &models.User{}, &models.UserOrganization{}, &models.WhatsAppAccount{}, &models.AuditLog{}, &models.SSOProvider{}))
	redis := testutil.SetupTestRedis(t)
	if redis == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	app := &App{DB: db, Redis: redis, Log: testutil.NopLogger(), Config: &config.Config{App: config.AppConfig{EncryptionKey: "test-managed-plan-encryption-key-long-enough"}, Plan: config.PlanConfig{Enabled: true, MaxUsers: 1, MaxNumbers: 1, MaxStorageBytes: 1024, SupportEmail: "support@example.test"}}}
	app.Config.JWT = config.JWTConfig{Secret: testutil.TestJWTSecret, AccessExpiryMins: 15, RefreshExpiryDays: 7}
	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, true, nil)
	owner := testutil.CreateTestUser(t, db, org.ID, testutil.WithEmail("owner@example.test"), testutil.WithSuperAdmin(), testutil.WithRoleID(&role.ID))
	support := testutil.CreateTestUser(t, db, org.ID, testutil.WithEmail("support@example.test"), testutil.WithSuperAdmin(), testutil.WithRoleID(&role.ID))
	require.NoError(t, plan.Install(db, app.Config.Plan))
	return app, org, owner, support
}

func TestManagedUserCreateAndRestoreReturn409(t *testing.T) {
	app, org, owner, _ := managedEntityApp(t)
	req := testutil.NewJSONRequest(t, UserRequest{Email: "new@example.test", Password: "password123", FullName: "New user"})
	testutil.SetAuthContext(req, org.ID, owner.ID)
	require.NoError(t, app.CreateUser(req))
	testutil.AssertErrorResponse(t, req, 409, "User limit reached")
	// A deleted record occupies no seat, but restoring it must reacquire a seat.
	require.NoError(t, app.DB.Exec("INSERT INTO users(id,email,organization_id,deleted_at) VALUES (?,?,?,now())", uuid.New(), "restore@example.test", org.ID).Error)
	req = testutil.NewJSONRequest(t, UserRequest{Email: "restore@example.test", Password: "password123", FullName: "Restored"})
	testutil.SetAuthContext(req, org.ID, owner.ID)
	require.NoError(t, app.CreateUser(req))
	testutil.AssertErrorResponse(t, req, 409, "User limit reached")
	var count int64
	require.NoError(t, app.DB.Model(&models.User{}).Where("email='restore@example.test'").Count(&count).Error)
	require.Zero(t, count)
}

func TestManagedRegisterReturns409AndRollsBack(t *testing.T) {
	app, org, _, _ := managedEntityApp(t)
	req := testutil.NewJSONRequest(t, RegisterRequest{Email: "register@example.test", Password: "password123", FullName: "Register", OrganizationID: org.ID})
	require.NoError(t, app.Register(req))
	testutil.AssertErrorResponse(t, req, 409, "User limit reached")
	var count int64
	require.NoError(t, app.DB.Model(&models.User{}).Where("email='register@example.test'").Count(&count).Error)
	require.Zero(t, count)
}

func TestManagedAccountCreateReturns409(t *testing.T) {
	app, org, owner, _ := managedEntityApp(t)
	require.NoError(t, app.DB.Create(&models.WhatsAppAccount{OrganizationID: org.ID, Name: "pending", Status: "pending_registration", PhoneID: "one", BusinessID: "business", AccessToken: "token"}).Error)
	req := testutil.NewJSONRequest(t, AccountRequest{Name: "new", PhoneID: "two", BusinessID: "business", AccessToken: "token"})
	testutil.SetAuthContext(req, org.ID, owner.ID)
	require.NoError(t, app.CreateAccount(req))
	testutil.AssertErrorResponse(t, req, 409, "WhatsApp number limit reached")
}

func TestManagedRejectedNumberPreservesExistingDefaults(t *testing.T) {
	app, org, owner, _ := managedEntityApp(t)
	existing := models.WhatsAppAccount{OrganizationID: org.ID, Name: "existing", PhoneID: "one", BusinessID: "business", AccessToken: "token", IsDefaultIncoming: true, IsDefaultOutgoing: true}
	require.NoError(t, app.DB.Create(&existing).Error)
	req := testutil.NewJSONRequest(t, AccountRequest{Name: "rejected", PhoneID: "two", BusinessID: "business", AccessToken: "token", IsDefaultIncoming: true, IsDefaultOutgoing: true})
	testutil.SetAuthContext(req, org.ID, owner.ID)
	require.NoError(t, app.CreateAccount(req))
	testutil.AssertErrorResponse(t, req, 409, "WhatsApp number limit reached")
	require.NoError(t, app.DB.First(&existing, existing.ID).Error)
	require.True(t, existing.IsDefaultIncoming)
	require.True(t, existing.IsDefaultOutgoing)
}

func TestManagedEmbeddedSignupRejectsBeforeExternalRegistration(t *testing.T) {
	app, org, owner, _ := managedEntityApp(t)
	require.NoError(t, app.DB.Create(&models.WhatsAppAccount{OrganizationID: org.ID, Name: "existing", PhoneID: "one", BusinessID: "business", AccessToken: "token"}).Error)
	var registrations, subscriptions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "oauth/access_token"):
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "token"})
		case strings.HasSuffix(r.URL.Path, "/register"):
			registrations.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
		case strings.HasSuffix(r.URL.Path, "/subscribed_apps"):
			subscriptions.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"verified_name": "New Number", "display_phone_number": "+123456789"})
		}
	}))
	defer server.Close()
	app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, server.URL)
	req := testutil.NewJSONRequest(t, map[string]string{"code": "test-code", "phone_id": "two", "waba_id": "business", "name": "new"})
	testutil.SetAuthContext(req, org.ID, owner.ID)
	require.NoError(t, app.ExchangeToken(req))
	testutil.AssertErrorResponse(t, req, 409, "WhatsApp number limit reached")
	require.Zero(t, registrations.Load())
	require.Zero(t, subscriptions.Load())
}

func TestManagedSupportCannotBeChangedThroughCustomerHandlers(t *testing.T) {
	app, org, owner, support := managedEntityApp(t)
	req := testutil.NewJSONRequest(t, UserRequest{FullName: "Customer"})
	testutil.SetAuthContext(req, org.ID, owner.ID)
	testutil.SetPathParam(req, "id", support.ID.String())
	require.NoError(t, app.UpdateUser(req))
	testutil.AssertErrorResponse(t, req, 403, "Support account is protected")
	req = testutil.NewRequest(t)
	testutil.SetAuthContext(req, org.ID, owner.ID)
	testutil.SetPathParam(req, "id", support.ID.String())
	require.NoError(t, app.DeleteUser(req))
	testutil.AssertErrorResponse(t, req, 403, "Support account is protected")
	req = testutil.NewJSONRequest(t, ChangePasswordRequest{CurrentPassword: "password123", NewPassword: "changed123"})
	testutil.SetAuthContext(req, org.ID, support.ID)
	require.NoError(t, app.ChangePassword(req))
	testutil.AssertErrorResponse(t, req, 403, "Support account is protected")
	req = testutil.NewRequest(t)
	testutil.SetAuthContext(req, org.ID, owner.ID)
	testutil.SetPathParam(req, "member_id", support.ID.String())
	require.NoError(t, app.RemoveOrganizationMember(req))
	testutil.AssertErrorResponse(t, req, 403, "Support account is protected")
}

func TestManagedSSOAutoCreationAndSupportIdentityAreProtected(t *testing.T) {
	app, org, _, _ := managedEntityApp(t)
	email := "new-sso@example.test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fake-token", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": "external-identity", "email": email, "name": "External user"})
	}))
	defer server.Close()
	app.Config.App.Environment = "development"
	app.HTTPClient = server.Client()
	require.NoError(t, app.DB.Create(&models.SSOProvider{OrganizationID: org.ID, Provider: "custom", ClientID: "test-client", ClientSecret: "test-secret", IsEnabled: true, AllowAutoCreate: true, DefaultRoleName: "admin", AuthURL: server.URL + "/auth", TokenURL: server.URL + "/token", UserInfoURL: server.URL + "/userinfo"}).Error)
	for _, tc := range []struct {
		email   string
		status  int
		message string
	}{{"new-sso@example.test", 409, "User limit reached"}, {"support@example.test", 403, "Support account is protected"}} {
		t.Run(tc.email, func(t *testing.T) {
			email = tc.email
			nonce := uuid.NewString()
			state, _ := json.Marshal(SSOState{OrgID: org.ID.String(), Provider: "custom", Nonce: nonce, ExpiresAt: time.Now().Add(time.Minute)})
			require.NoError(t, app.Redis.Set(context.Background(), "sso:state:"+nonce, state, time.Minute).Err())
			req := testutil.NewGETRequest(t)
			testutil.SetPathParam(req, "provider", "custom")
			testutil.SetQueryParam(req, "code", "test-code")
			testutil.SetQueryParam(req, "state", nonce)
			require.NoError(t, app.CallbackSSO(req))
			testutil.AssertErrorResponse(t, req, tc.status, tc.message)
			require.Empty(t, req.RequestCtx.Response.Header.Peek("Set-Cookie"))
		})
	}
}

func TestManagedSupportCanStillUsePasswordLoginAtCapacity(t *testing.T) {
	app, _, _, _ := managedEntityApp(t)
	req := testutil.NewJSONRequest(t, LoginRequest{Email: "support@example.test", Password: "password123"})
	require.NoError(t, app.Login(req))
	require.Equal(t, 200, testutil.GetResponseStatusCode(req))
	require.NotEmpty(t, req.RequestCtx.Response.Header.Peek("Set-Cookie"))
}
