package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

const auditTestPassword = "Synthetic audit password 123!"

func newInboxAuditAuthFixture(t *testing.T) inboxAuditFixture {
	t.Helper()
	f := newInboxAuditFixture(t)
	f.app.Config = &config.Config{App: config.AppConfig{Environment: "test"}, JWT: config.JWTConfig{Secret: testutil.TestJWTSecret, AccessExpiryMins: 15, RefreshExpiryDays: 7}}
	f.app.Redis = testutil.SetupTestRedis(t)
	require.NotNil(t, f.app.Redis)
	f.app.HTTPClient = &http.Client{Timeout: 3 * time.Second}
	hash, err := bcrypt.GenerateFromPassword([]byte(auditTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, f.db.Model(f.user).Update("password_hash", string(hash)).Error)
	return f
}

func (f inboxAuditFixture) securityRows(t *testing.T) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	require.NoError(t, f.db.Where("organization_id=? AND context_version=1", f.org.ID).Order("created_at,id").Find(&rows).Error)
	return rows
}

func auditLoginRequest(t *testing.T, email, password string) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]string{"email": email, "password": password})
	r.RequestCtx.SetRemoteAddr(&net.TCPAddr{IP: net.ParseIP("203.0.113.19"), Port: 12345})
	r.RequestCtx.Request.Header.Set("X-Forwarded-For", "198.51.100.200")
	r.RequestCtx.Request.Header.Set("X-Request-ID", "synthetic-untrusted-correlation")
	return r
}

func TestInboxAuditRequestContextUsesServerIdentityAndQualifiedProxyOnly(t *testing.T) {
	app := &App{Config: &config.Config{}}
	r := auditLoginRequest(t, "synthetic@example.invalid", "unused")
	first := app.requestAuditContext(r)
	require.NotEqual(t, uuid.Nil, first.RequestID)
	require.Equal(t, audit.Origin{IP: "203.0.113.19", Source: "peer"}, first.Origin)
	// Mutating headers after capture cannot change provenance within the request.
	r.RequestCtx.Request.Header.Set("X-Forwarded-For", "192.0.2.1")
	require.Equal(t, first, app.requestAuditContext(r))
	second := app.requestAuditContext(auditLoginRequest(t, "synthetic@example.invalid", "unused"))
	require.NotEqual(t, first.RequestID, second.RequestID)
	app.Config.Audit.TrustedProxyCIDRs = "203.0.113.19/32"
	forwarded := app.requestAuditContext(auditLoginRequest(t, "synthetic@example.invalid", "unused"))
	require.Equal(t, audit.Origin{IP: "198.51.100.200", Source: "forwarded"}, forwarded.Origin)
	app.Config.Audit.TrustedProxyCIDRs = "0.0.0.0/0"
	fallback := app.requestAuditContext(auditLoginRequest(t, "synthetic@example.invalid", "unused"))
	require.Equal(t, audit.Origin{IP: "203.0.113.19", Source: "peer"}, fallback.Origin)
}

func TestInboxAuditPasswordLoginCapturesExactOutcomeWithoutCredentials(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	r := auditLoginRequest(t, f.user.Email, auditTestPassword)
	require.NoError(t, f.app.Login(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.NotEmpty(t, testutil.GetResponseCookie(r, cookieAccessName))
	rows := f.securityRows(t)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, string(audit.SignIn), row.Event)
	require.Equal(t, string(audit.Success), row.Outcome)
	require.Equal(t, string(audit.Password), row.Reason)
	require.True(t, row.ActorVerified)
	require.Equal(t, f.user.ID, row.UserID)
	require.Equal(t, f.user.ID, row.ResourceID)
	require.Equal(t, "203.0.113.19", row.OriginIP)
	require.Equal(t, "peer", row.OriginSource)
	require.NotEqual(t, "synthetic-untrusted-correlation", row.RequestID)
	f.app.observeSignIn(r, f.org.ID, f.user.ID, f.user.ID, audit.Success, audit.Password)
	require.Len(t, f.securityRows(t), 1, "same request exact capture is idempotent")
	other := auditLoginRequest(t, f.user.Email, auditTestPassword)
	require.NoError(t, f.app.Login(other))
	rows = f.securityRows(t)
	require.Len(t, rows, 2)
	require.NotEqual(t, rows[0].RequestID, rows[1].RequestID)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), auditTestPassword)
	require.NotContains(t, string(encoded), testutil.GetResponseCookie(r, cookieRefreshName))
	require.Empty(t, rows[0].Changes)
}

func TestInboxAuditDeniedLoginDoesNotVerifyActorOrInventTenant(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	wrong := auditLoginRequest(t, f.user.Email, "Synthetic incorrect password")
	require.NoError(t, f.app.Login(wrong))
	require.Equal(t, 401, wrong.RequestCtx.Response.StatusCode())
	rows := f.securityRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, uuid.Nil, rows[0].UserID)
	require.False(t, rows[0].ActorVerified)
	require.Equal(t, string(audit.InvalidCredentials), rows[0].Reason)
	require.NoError(t, f.db.Model(f.user).Update("is_active", false).Error)
	disabled := auditLoginRequest(t, f.user.Email, auditTestPassword)
	require.NoError(t, f.app.Login(disabled))
	require.Equal(t, 401, disabled.RequestCtx.Response.StatusCode())
	rows = f.securityRows(t)
	require.Len(t, rows, 2)
	require.Equal(t, string(audit.DisabledAccount), rows[1].Reason)
	require.False(t, rows[1].ActorVerified)
	unknown := auditLoginRequest(t, uuid.NewString()+"@example.invalid", auditTestPassword)
	unknown.RequestCtx.Request.Header.Set("X-Organization-ID", f.org.ID.String())
	require.NoError(t, f.app.Login(unknown))
	require.Equal(t, 401, unknown.RequestCtx.Response.StatusCode())
	require.Len(t, f.securityRows(t), 2)
}

func TestInboxAuditSessionCreationFailureRecordsFailureWithoutChangingResponse(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	f.app.Redis = nil
	r := auditLoginRequest(t, f.user.Email, auditTestPassword)
	require.NoError(t, f.app.Login(r))
	require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
	require.Empty(t, testutil.GetResponseCookie(r, cookieAccessName))
	rows := f.securityRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, string(audit.Failed), rows[0].Outcome)
	require.Equal(t, string(audit.SessionCreationFailed), rows[0].Reason)
	require.False(t, rows[0].ActorVerified)
}

type auditLockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *auditLockedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *auditLockedLog) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestInboxAuditCaptureFailureKeepsSuccessfulLoginAndLogsOnlySafeCode(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	name := "audit_capture_fault_" + uuid.NewString()
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.OrganizationID == f.org.ID {
			_ = db.AddError(errors.New("synthetic-private-credential-database-error"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.db.Callback().Create().Remove(name)) })
	var logs auditLockedLog
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r := auditLoginRequest(t, f.user.Email, auditTestPassword)
	require.NoError(t, f.app.Login(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	require.NotEmpty(t, testutil.GetResponseCookie(r, cookieAccessName))
	require.Empty(t, f.securityRows(t))
	require.Contains(t, logs.text(), "storage_unavailable")
	require.NotContains(t, logs.text(), "synthetic-private-credential")
	require.NotContains(t, logs.text(), auditTestPassword)
}

func TestInboxAuditLogoutCapturesRevocationRepeatedAndUnavailableStorage(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	login := auditLoginRequest(t, f.user.Email, auditTestPassword)
	require.NoError(t, f.app.Login(login))
	refresh := testutil.GetResponseCookie(login, cookieRefreshName)
	for _, reason := range []audit.Reason{audit.SignedOut, audit.AlreadySignedOut} {
		r := testutil.NewJSONRequest(t, LogoutRequest{RefreshToken: refresh})
		require.NoError(t, f.app.Logout(r))
		require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
		rows := f.securityRows(t)
		row := rows[len(rows)-1]
		require.Equal(t, string(audit.SignOut), row.Event)
		require.Equal(t, string(reason), row.Reason)
		require.True(t, row.ActorVerified)
	}
	f.app.Redis = nil
	r := testutil.NewJSONRequest(t, LogoutRequest{RefreshToken: refresh})
	require.NoError(t, f.app.Logout(r))
	require.Equal(t, 503, r.RequestCtx.Response.StatusCode())
	rows := f.securityRows(t)
	require.Len(t, rows, 4)
	require.Equal(t, string(audit.Failed), rows[3].Outcome)
	require.Equal(t, string(audit.SessionStorageUnavailable), rows[3].Reason)
	invalid := testutil.NewJSONRequest(t, LogoutRequest{RefreshToken: "synthetic-invalid-token"})
	require.NoError(t, f.app.Logout(invalid))
	require.Equal(t, 200, invalid.RequestCtx.Response.StatusCode())
	require.Len(t, f.securityRows(t), 4)
}

func TestInboxAuditSSOActualCallbackQualifiesMicrosoftAndGenericOutcomes(t *testing.T) {
	for _, provider := range []string{"microsoft", "custom"} {
		for _, state := range []string{"active", "disabled", "unlinked", "foreign"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				f := newInboxAuditAuthFixture(t)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/token" {
						_, _ = w.Write([]byte(`{"access_token":"synthetic-provider-token","token_type":"Bearer","expires_in":300}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"sub": "synthetic-subject", "id": "synthetic-subject", "mail": f.user.Email, "email": f.user.Email, "displayName": "Synthetic identity", "name": "Synthetic identity"})
				}))
				t.Cleanup(server.Close)
				p := models.SSOProvider{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Provider: provider, ClientID: "synthetic-client", ClientSecret: "synthetic-secret", IsEnabled: true, AllowAutoCreate: false, AuthURL: server.URL + "/auth", TokenURL: server.URL + "/token", UserInfoURL: server.URL + "/userinfo"}
				require.NoError(t, f.db.Create(&p).Error)
				if provider == "microsoft" {
					original := oauthProviders[provider]
					fake := original
					fake.Endpoint = oauth2.Endpoint{AuthURL: p.AuthURL, TokenURL: p.TokenURL}
					fake.UserInfoURL = p.UserInfoURL
					oauthProviders[provider] = fake
					t.Cleanup(func() { oauthProviders[provider] = original })
				}
				updates := map[string]any{"sso_provider": provider, "sso_provider_id": "synthetic-subject", "sso_binding": SSOProviderBinding(&p)}
				if state == "disabled" {
					updates["is_active"] = false
				}
				if state == "unlinked" {
					updates["sso_binding"] = ""
				}
				if state == "foreign" {
					other := testutil.CreateTestOrganization(t, f.db)
					updates["organization_id"] = other.ID
				}
				require.NoError(t, f.db.Model(f.user).Updates(updates).Error)
				nonce := uuid.NewString()
				encoded, err := json.Marshal(SSOState{ConfigBinding: SSOProviderBinding(&p), OrgID: f.org.ID.String(), Provider: provider, Nonce: nonce, ExpiresAt: time.Now().Add(time.Minute)})
				require.NoError(t, err)
				require.NoError(t, f.app.Redis.Set(context.Background(), "sso:state:"+nonce, encoded, time.Minute).Err())
				r := testutil.NewGETRequest(t)
				testutil.SetPathParam(r, "provider", provider)
				testutil.SetQueryParam(r, "state", nonce)
				testutil.SetQueryParam(r, "code", "synthetic-private-oauth-code")
				require.NoError(t, f.app.CallbackSSO(r))
				require.Equal(t, 307, r.RequestCtx.Response.StatusCode())
				rows := f.securityRows(t)
				if state == "foreign" {
					require.Empty(t, rows)
					var otherTenantEvents int64
					require.NoError(t, f.db.Model(&models.AuditLog{}).Where("resource_id=? AND context_version=1", f.user.ID).Count(&otherTenantEvents).Error)
					require.Zero(t, otherTenantEvents)
					return
				}
				require.Len(t, rows, 1)
				row := rows[0]
				if state == "active" {
					expected := audit.FederatedSSO
					if provider == "microsoft" {
						expected = audit.MicrosoftSSO
					}
					require.Equal(t, string(expected), row.Reason)
					require.Equal(t, string(audit.Success), row.Outcome)
					require.True(t, row.ActorVerified)
					require.NotEmpty(t, testutil.GetResponseCookie(r, cookieAccessName))
				} else {
					require.Equal(t, string(audit.Denied), row.Outcome)
					require.False(t, row.ActorVerified)
					require.Empty(t, testutil.GetResponseCookie(r, cookieAccessName))
				}
				raw, err := json.Marshal(row)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "synthetic-provider-token")
				require.NotContains(t, string(raw), "synthetic-private-oauth-code")
				require.NotContains(t, string(raw), "synthetic-secret")
			})
		}
	}
}

func TestInboxAuditPermissionDenialDeduplicatesAndRejectsStaleActor(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	role := testutil.CreateAgentRole(t, f.db, f.org.ID)
	user := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, f.org.ID, user.ID)
	r.RequestCtx.SetUserValue(middleware.ContextKeyClaims, &middleware.JWTClaims{UserID: user.ID, OrganizationID: f.org.ID, SessionID: "synthetic-session", AuthStamp: user.AuthStamp})
	_, _, err := f.app.requireAuth(r, models.ResourceRoles, models.ActionWrite)
	require.Error(t, err)
	require.Equal(t, 403, r.RequestCtx.Response.StatusCode())
	f.app.observeDenial(r, f.org.ID, user.ID, models.ResourceRoles, models.ActionWrite)
	rows := f.securityRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, string(audit.AuthorizationDenied), rows[0].Event)
	require.Equal(t, "roles:write", rows[0].Permission)
	require.Equal(t, f.org.ID, rows[0].ResourceID)
	priorStamp := user.AuthStamp
	require.NoError(t, f.db.Model(user).Update("auth_stamp", uuid.NewString()).Error)
	stale := testutil.NewGETRequest(t)
	testutil.SetAuthContext(stale, f.org.ID, user.ID)
	stale.RequestCtx.SetUserValue(middleware.ContextKeyClaims, &middleware.JWTClaims{UserID: user.ID, OrganizationID: f.org.ID, SessionID: "synthetic-session", AuthStamp: priorStamp})
	f.app.observeDenial(stale, f.org.ID, user.ID, models.ResourceRoles, models.ActionWrite)
	require.Len(t, f.securityRows(t), 1)
}
