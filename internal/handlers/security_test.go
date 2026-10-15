package handlers_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestSecuritySSO_DoesNotTrustEmailAlone(t *testing.T) {
	for _, mode := range []string{"native", "legacy-unbound", "subject-mismatch", "provider-mismatch", "other-organization", "missing-subject", "replaced-config", "changed-during-flow", "disabled-config"} {
		t.Run(mode, func(t *testing.T) {
			app := newSSOApp(t)
			fake := newFakeOAuth(t)
			org := testutil.CreateTestOrganization(t, app.DB)
			fake.UserEmail = testutil.UniqueEmail("sso-takeover")
			provider := createCustomSSOProvider(t, app, org.ID, fake)
			t.Cleanup(func() { app.DB.Unscoped().Delete(provider) })
			user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(fake.UserEmail))
			if mode != "native" {
				user.SSOProvider = "custom"
				user.SSOProviderID = fake.UserID
				user.SSOBinding = handlers.SSOProviderBinding(provider)
			}
			switch mode {
			case "legacy-unbound":
				user.SSOBinding = ""
			case "replaced-config":
				provider.ClientID = "replaced-client"
				require.NoError(t, app.DB.Save(provider).Error)
			case "subject-mismatch":
				user.SSOProviderID = "different"
			case "provider-mismatch":
				user.SSOProvider = "google"
			case "other-organization":
				user.OrganizationID = testutil.CreateTestOrganization(t, app.DB).ID
			case "missing-subject":
				fake.UserID = ""
			}
			require.NoError(t, app.DB.Save(user).Error)
			nonce := uuid.NewString()
			state := handlers.SSOState{ConfigBinding: handlers.SSOProviderBinding(provider), OrgID: org.ID.String(), Provider: "custom", Nonce: nonce, ExpiresAt: time.Now().Add(time.Minute)}
			encoded, _ := json.Marshal(state)
			require.NoError(t, app.Redis.Set(context.Background(), "sso:state:"+nonce, encoded, time.Minute).Err())
			if mode == "changed-during-flow" {
				require.NoError(t, app.DB.Model(provider).Update("client_id", "changed-client").Error)
			}
			if mode == "disabled-config" {
				require.NoError(t, app.DB.Model(provider).Update("is_enabled", false).Error)
			}
			req := testutil.NewGETRequest(t)
			testutil.SetPathParam(req, "provider", "custom")
			testutil.SetQueryParam(req, "code", "fake-code")
			testutil.SetQueryParam(req, "state", nonce)
			require.NoError(t, app.CallbackSSO(req))
			require.Empty(t, testutil.GetResponseCookie(req, "whm_access"), "unbound identity cannot authenticate existing account")
			require.Contains(t, string(req.RequestCtx.Response.Header.Peek("Location")), "sso_error=")
		})
	}
}

func TestSecuritySSO_ExplicitAdministratorBinding(t *testing.T) {
	app := newSSOApp(t)
	fake := newFakeOAuth(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	provider := createCustomSSOProvider(t, app, org.ID, fake)
	t.Cleanup(func() { app.DB.Unscoped().Delete(provider) })
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	req := testutil.NewJSONRequest(t, map[string]string{"sso_config_id": provider.ID.String(), "sso_subject": fake.UserID})
	testutil.SetAuthContext(req, org.ID, admin.ID)
	testutil.SetPathParam(req, "id", user.ID.String())
	require.NoError(t, app.UpdateUser(req))
	require.Equal(t, 200, req.RequestCtx.Response.StatusCode())
	require.NoError(t, app.DB.First(user, "id = ?", user.ID).Error)
	require.Equal(t, handlers.SSOProviderBinding(provider), user.SSOBinding)
	require.Equal(t, fake.UserID, user.SSOProviderID)
	fake.UserEmail = user.Email
	nonce := uuid.NewString()
	state := handlers.SSOState{ConfigBinding: handlers.SSOProviderBinding(provider), OrgID: org.ID.String(), Provider: "custom", Nonce: nonce, ExpiresAt: time.Now().Add(time.Minute)}
	encoded, _ := json.Marshal(state)
	require.NoError(t, app.Redis.Set(context.Background(), "sso:state:"+nonce, encoded, time.Minute).Err())
	callback := testutil.NewGETRequest(t)
	testutil.SetPathParam(callback, "provider", "custom")
	testutil.SetQueryParam(callback, "code", "code")
	testutil.SetQueryParam(callback, "state", nonce)
	require.NoError(t, app.CallbackSSO(callback))
	require.NotEmpty(t, testutil.GetResponseCookie(callback, "whm_access"))
}

func TestSecuritySSO_DoesNotCreateEmptySubjectUser(t *testing.T) {
	app := newSSOApp(t)
	fake := newFakeOAuth(t)
	fake.UserID = ""
	fake.UserEmail = testutil.UniqueEmail("missing-sso-subject")
	org := testutil.CreateTestOrganization(t, app.DB)
	testutil.CreateTestRoleExact(t, app.DB, org.ID, "agent", true, true, nil)
	provider := createCustomSSOProvider(t, app, org.ID, fake)
	t.Cleanup(func() { app.DB.Unscoped().Delete(provider) })
	nonce := uuid.NewString()
	state := handlers.SSOState{ConfigBinding: handlers.SSOProviderBinding(provider), OrgID: org.ID.String(), Provider: "custom", Nonce: nonce, ExpiresAt: time.Now().Add(time.Minute)}
	encoded, _ := json.Marshal(state)
	require.NoError(t, app.Redis.Set(context.Background(), "sso:state:"+nonce, encoded, time.Minute).Err())
	req := testutil.NewGETRequest(t)
	testutil.SetPathParam(req, "provider", "custom")
	testutil.SetQueryParam(req, "code", "code")
	testutil.SetQueryParam(req, "state", nonce)
	require.NoError(t, app.CallbackSSO(req))
	require.Empty(t, testutil.GetResponseCookie(req, "whm_access"))
	var count int64
	require.NoError(t, app.DB.Model(&models.User{}).Where("email = ?", fake.UserEmail).Count(&count).Error)
	require.Zero(t, count)
}

func TestSecurity_DisabledAPIKeyAndCachedPermissions(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	require.True(t, app.HasPermission(user.ID, models.ResourceUsers, models.ActionWrite, org.ID))
	key := "whm_0123456789abcdef0123456789abcdef"
	hash, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, app.DB.Create(&models.APIKey{UserID: user.ID, OrganizationID: org.ID, Name: "security-key", KeyPrefix: key[4:20], KeyHash: string(hash), IsActive: true}).Error)
	require.NoError(t, app.DB.Model(user).Update("is_active", false).Error)
	req := testutil.NewGETRequest(t)
	req.RequestCtx.Request.Header.Set("X-API-Key", key)
	require.Nil(t, middleware.AuthWithDB(testutil.TestJWTSecret, app.DB)(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode())
	require.False(t, app.HasPermission(user.ID, models.ResourceUsers, models.ActionWrite, org.ID), "cached permissions cannot revive disabled account")
}
