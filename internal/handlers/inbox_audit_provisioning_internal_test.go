package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func auditRegistrationRequest(t *testing.T, f inboxAuditFixture, email string) *fastglue.Request {
	t.Helper()
	return testutil.NewJSONRequest(t, RegisterRequest{Email: email, Password: auditTestPassword, FullName: "Synthetic registered user", OrganizationID: f.org.ID})
}

func TestInboxAuditRegistrationSeparatesPolicyGrantFromAuthenticatedSession(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "join"}[existing], func(t *testing.T) {
			f := newInboxAuditAuthFixture(t)
			role := testutil.CreateTestRoleExact(t, f.db, f.org.ID, "agent", true, true, nil)
			email := testutil.UniqueEmail("audit-registration")
			var original *models.User
			if existing {
				home := testutil.CreateTestOrganization(t, f.db)
				original = testutil.CreateTestUser(t, f.db, home.ID, testutil.WithEmail(email), testutil.WithPassword(auditTestPassword))
			}
			r := auditRegistrationRequest(t, f, email)
			require.NoError(t, f.app.Register(r))
			require.Equal(t, 200, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
			var current models.User
			require.NoError(t, f.db.First(&current, "email=?", email).Error)
			var membership models.UserOrganization
			require.NoError(t, f.db.First(&membership, "user_id=? AND organization_id=?", current.ID, f.org.ID).Error)
			require.Equal(t, role.ID, *membership.RoleID)
			require.Equal(t, !existing, membership.IsDefault)
			if existing {
				require.Equal(t, original.OrganizationID, current.OrganizationID)
				require.True(t, sameAuditRole(original.RoleID, current.RoleID))
			}
			rows := f.securityRows(t)
			require.Len(t, rows, 2)
			require.Equal(t, string(audit.IdentityProvisioned), rows[0].Event)
			require.Equal(t, uuid.Nil, rows[0].UserID)
			require.Equal(t, "Provisioning policy", rows[0].UserName)
			require.False(t, rows[0].ActorVerified)
			reason := audit.RegistrationCreated
			if existing {
				reason = audit.RegistrationJoined
			}
			require.Equal(t, string(reason), rows[0].Reason)
			require.Equal(t, current.ID, rows[0].ResourceID)
			require.Equal(t, string(audit.SignIn), rows[1].Event)
			require.Equal(t, string(audit.Success), rows[1].Outcome)
			require.Equal(t, current.ID, rows[1].UserID)
			require.True(t, rows[1].ActorVerified)
			require.Equal(t, rows[0].RequestID, rows[1].RequestID)
			claims, err := middleware.ParseToken(f.app.Config.JWT.Secret, testutil.GetResponseCookie(r, cookieAccessName), "access")
			require.NoError(t, err)
			require.Equal(t, f.org.ID, claims.OrganizationID)
			require.Equal(t, role.ID, *claims.RoleID)
			raw, err := json.Marshal(rows)
			require.NoError(t, err)
			require.NotContains(t, string(raw), auditTestPassword)
			require.NotContains(t, string(raw), current.PasswordHash)
		})
	}
}

func TestInboxAuditRegistrationCaptureFailureRollsBackIdentityAndMembership(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "join"}[existing], func(t *testing.T) {
			f := newInboxAuditAuthFixture(t)
			testutil.CreateTestRoleExact(t, f.db, f.org.ID, "agent", true, true, nil)
			email := testutil.UniqueEmail("audit-failed-registration")
			var original *models.User
			if existing {
				home := testutil.CreateTestOrganization(t, f.db)
				original = testutil.CreateTestUser(t, f.db, home.ID, testutil.WithEmail(email), testutil.WithPassword(auditTestPassword))
			}
			calls := failIntegrationCapture(t, f, audit.IdentityProvisioned)
			var logs auditLockedLog
			f.app.DB = f.db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Info})})
			var one int
			require.NoError(t, f.app.DB.Raw("SELECT 1").Scan(&one).Error)
			require.Contains(t, logs.text(), "SELECT")
			r := auditRegistrationRequest(t, f, email)
			require.NoError(t, f.app.Register(r))
			require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
			require.EqualValues(t, 1, calls.Load())
			var count int64
			require.NoError(t, f.db.Model(&models.UserOrganization{}).Joins("JOIN users ON users.id=user_organizations.user_id").Where("users.email=? AND user_organizations.organization_id=?", email, f.org.ID).Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, f.db.Model(&models.User{}).Where("email=?", email).Count(&count).Error)
			if existing {
				require.EqualValues(t, 1, count)
				var retained models.User
				require.NoError(t, f.db.First(&retained, "id=?", original.ID).Error)
				require.Equal(t, original.PasswordHash, retained.PasswordHash)
				require.Equal(t, original.OrganizationID, retained.OrganizationID)
			} else {
				require.Zero(t, count)
			}
			require.Empty(t, f.securityRows(t))
			require.Empty(t, testutil.GetResponseCookie(r, cookieAccessName))
			require.Empty(t, testutil.GetResponseCookie(r, cookieRefreshName))
			require.NotContains(t, logs.text(), auditTestPassword)
			require.NotContains(t, logs.text(), "$2a$")
			require.NotContains(t, logs.text(), "$2b$")
		})
	}
}

func TestInboxAuditRegistrationSessionFailureRetainsOnlyCommittedProvisioningAndFailure(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	testutil.CreateTestRoleExact(t, f.db, f.org.ID, "agent", true, true, nil)
	f.app.Redis = nil
	r := auditRegistrationRequest(t, f, testutil.UniqueEmail("audit-session-failure"))
	require.NoError(t, f.app.Register(r))
	require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
	rows := f.securityRows(t)
	require.Len(t, rows, 2)
	require.Equal(t, string(audit.IdentityProvisioned), rows[0].Event)
	require.Equal(t, string(audit.SignIn), rows[1].Event)
	require.Equal(t, string(audit.Failed), rows[1].Outcome)
	require.False(t, rows[1].ActorVerified)
	require.Empty(t, testutil.GetResponseCookie(r, cookieAccessName))
	require.Empty(t, testutil.GetResponseCookie(r, cookieRefreshName))
}

func TestInboxAuditRegistrationConcurrentJoinsCommitOneMembership(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	testutil.CreateTestRoleExact(t, f.db, f.org.ID, "agent", true, true, nil)
	home := testutil.CreateTestOrganization(t, f.db)
	u := testutil.CreateTestUser(t, f.db, home.ID, testutil.WithPassword(auditTestPassword))
	requests := []*fastglue.Request{auditRegistrationRequest(t, f, u.Email), auditRegistrationRequest(t, f, u.Email)}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, r := range requests {
		wg.Add(1)
		go func(r *fastglue.Request) { defer wg.Done(); <-start; results <- f.app.Register(r) }(r)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	codes := []int{requests[0].RequestCtx.Response.StatusCode(), requests[1].RequestCtx.Response.StatusCode()}
	sort.Ints(codes)
	require.Equal(t, []int{200, 409}, codes)
	var count int64
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", u.ID, f.org.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	rows := f.securityRows(t)
	require.Len(t, rows, 2)
}

func TestInboxAuditRegistrationDoesNotReviveMembershipOrCrossPasswordReset(t *testing.T) {
	for _, mode := range []string{"revoked", "reset"} {
		t.Run(mode, func(t *testing.T) {
			f := newInboxAuditAuthFixture(t)
			role := testutil.CreateTestRoleExact(t, f.db, f.org.ID, "agent", true, true, nil)
			home := testutil.CreateTestOrganization(t, f.db)
			u := testutil.CreateTestUser(t, f.db, home.ID, testutil.WithPassword(auditTestPassword))
			seen := false
			if mode == "revoked" {
				m := models.UserOrganization{UserID: u.ID, OrganizationID: f.org.ID, RoleID: &role.ID}
				require.NoError(t, f.db.Create(&m).Error)
				require.NoError(t, f.db.Delete(&m).Error)
			} else {
				name := "registration_reset_race_" + uuid.NewString()
				require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
					lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
					if seen || !ok || lock.Strength != "UPDATE" || tx.Statement.Table != "users" {
						return
					}
					seen = true
					require.NoError(t, f.db.Model(&models.User{}).Where("id=?", u.ID).Updates(map[string]any{"auth_stamp": uuid.NewString(), "password_hash": "synthetic-replaced-hash"}).Error)
				}))
				t.Cleanup(func() { require.NoError(t, f.db.Callback().Query().Remove(name)) })
			}
			r := auditRegistrationRequest(t, f, u.Email)
			require.NoError(t, f.app.Register(r))
			require.Equal(t, 409, r.RequestCtx.Response.StatusCode())
			if mode == "reset" {
				require.True(t, seen)
				var retained models.User
				require.NoError(t, f.db.First(&retained, "id=?", u.ID).Error)
				require.Equal(t, "synthetic-replaced-hash", retained.PasswordHash)
			}
			var count int64
			require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", u.ID, f.org.ID).Count(&count).Error)
			require.Zero(t, count)
			require.Empty(t, f.securityRows(t))
			require.Empty(t, testutil.GetResponseCookie(r, cookieAccessName))
		})
	}
}

func TestInboxAuditSSOProvisioningIsAtomicAndRechecksPolicyAfterProviderCall(t *testing.T) {
	for _, provider := range []string{"microsoft", "custom"} {
		for _, mode := range []string{"success", "capture_failure", "membership_failure", "policy_changed", "session_failure"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				f := newInboxAuditAuthFixture(t)
				role := testutil.CreateTestRoleExact(t, f.db, f.org.ID, "agent", true, true, nil)
				email := testutil.UniqueEmail("audit-sso-provision")
				var p models.SSOProvider
				var lookups atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/token" {
						_, _ = w.Write([]byte(`{"access_token":"synthetic-private-provider-token","token_type":"Bearer","expires_in":300}`))
						return
					}
					lookups.Add(1)
					if mode == "policy_changed" {
						require.NoError(t, f.db.Model(&models.SSOProvider{}).Where("id=?", p.ID).Update("allow_auto_create", false).Error)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"sub": "synthetic-subject", "id": "synthetic-subject", "mail": email, "email": email, "displayName": "Synthetic provisioned identity", "name": "Synthetic provisioned identity"})
				}))
				t.Cleanup(server.Close)
				p = models.SSOProvider{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Provider: provider, ClientID: "synthetic-client", ClientSecret: "synthetic-private-provider-secret", IsEnabled: true, AllowAutoCreate: true, DefaultRoleName: role.Name, AuthURL: server.URL + "/auth", TokenURL: server.URL + "/token", UserInfoURL: server.URL + "/userinfo"}
				require.NoError(t, f.db.Create(&p).Error)
				require.NoError(t, f.db.First(&p, "id=?", p.ID).Error)
				if provider == "microsoft" {
					original := oauthProviders[provider]
					fake := original
					fake.Endpoint = oauth2.Endpoint{AuthURL: p.AuthURL, TokenURL: p.TokenURL}
					fake.UserInfoURL = p.UserInfoURL
					oauthProviders[provider] = fake
					t.Cleanup(func() { oauthProviders[provider] = original })
				}
				if mode == "capture_failure" {
					failIntegrationCapture(t, f, audit.IdentityProvisioned)
				}
				if mode == "membership_failure" {
					name := "sso_membership_fault_" + uuid.NewString()
					require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
						if m, ok := tx.Statement.Dest.(*models.UserOrganization); ok && m.OrganizationID == f.org.ID {
							_ = tx.AddError(errors.New("synthetic-private-membership-error"))
						}
					}))
					t.Cleanup(func() { require.NoError(t, f.db.Callback().Create().Remove(name)) })
				}
				nonce := uuid.NewString()
				encoded, err := json.Marshal(SSOState{ConfigBinding: SSOProviderBinding(&p), OrgID: f.org.ID.String(), Provider: provider, Nonce: nonce, ExpiresAt: time.Now().Add(time.Minute)})
				require.NoError(t, err)
				require.NoError(t, f.app.Redis.Set(context.Background(), "sso:state:"+nonce, encoded, time.Minute).Err())
				r := testutil.NewGETRequest(t)
				testutil.SetPathParam(r, "provider", provider)
				testutil.SetQueryParam(r, "state", nonce)
				testutil.SetQueryParam(r, "code", "synthetic-private-oauth-code")
				if mode == "session_failure" {
					name := "sso_session_fault_" + uuid.NewString()
					require.NoError(t, f.db.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
						if row, ok := tx.Statement.Dest.(*models.AuditLog); ok && row.Event == string(audit.IdentityProvisioned) && row.OrganizationID == f.org.ID {
							f.app.Redis = nil
						}
					}))
					t.Cleanup(func() { require.NoError(t, f.db.Callback().Create().Remove(name)) })
				}
				require.NoError(t, f.app.CallbackSSO(r))
				require.Equal(t, 307, r.RequestCtx.Response.StatusCode())
				require.EqualValues(t, 1, lookups.Load())
				rows := f.securityRows(t)
				var users []models.User
				require.NoError(t, f.db.Where("email=?", email).Find(&users).Error)
				if mode == "success" || mode == "session_failure" {
					require.Len(t, users, 1)
					require.Len(t, rows, 2)
					require.Equal(t, string(audit.IdentityProvisioned), rows[0].Event)
					require.Equal(t, string(audit.SSOProvisioned), rows[0].Reason)
					require.False(t, rows[0].ActorVerified)
					require.Equal(t, uuid.Nil, rows[0].UserID)
					var m models.UserOrganization
					require.NoError(t, f.db.First(&m, "user_id=? AND organization_id=?", users[0].ID, f.org.ID).Error)
					require.Equal(t, role.ID, *m.RoleID)
					if mode == "success" {
						require.True(t, rows[1].ActorVerified)
						require.Equal(t, string(audit.Success), rows[1].Outcome)
						require.NotEmpty(t, testutil.GetResponseCookie(r, cookieAccessName))
					} else {
						require.Equal(t, string(audit.Failed), rows[1].Outcome)
						require.False(t, rows[1].ActorVerified)
						require.Empty(t, testutil.GetResponseCookie(r, cookieAccessName))
					}
				} else {
					require.Empty(t, users)
					require.Empty(t, rows)
					require.Empty(t, testutil.GetResponseCookie(r, cookieAccessName))
					var count int64
					require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id<>?", f.org.ID, f.user.ID).Count(&count).Error)
					require.Zero(t, count)
				}
				raw, err := json.Marshal(rows)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "synthetic-private-")
				require.NotContains(t, string(r.RequestCtx.Response.Body()), "synthetic-private-")
			})
		}
	}
}

func TestInboxAuditProvisioningRejectsInventedActorTargetOrAuthority(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	role := testutil.CreateTestRoleExact(t, f.db, f.org.ID, "agent", true, true, nil)
	u := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	base := audit.SecurityEvent{OrganizationID: f.org.ID, ResourceType: "user", ResourceID: u.ID, Kind: audit.IdentityProvisioned, Outcome: audit.Success, Reason: audit.RegistrationCreated, Context: f.event().Context}
	for _, mutate := range []func(*audit.SecurityEvent){func(e *audit.SecurityEvent) { e.ActorID = f.user.ID }, func(e *audit.SecurityEvent) { e.Outcome = audit.Failed }, func(e *audit.SecurityEvent) { e.ResourceType = "role" }, func(e *audit.SecurityEvent) { e.ResourceID = uuid.New() }, func(e *audit.SecurityEvent) { e.Reason = audit.MembershipAdded }, func(e *audit.SecurityEvent) { id := uuid.New(); e.Context.AccountID = &id }} {
		e := base
		mutate(&e)
		require.ErrorIs(t, audit.RecordSecurity(f.db, e), audit.ErrInvalidEvent)
	}
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", u.ID, f.org.ID).Update("role_id", nil).Error)
	require.ErrorIs(t, audit.RecordSecurity(f.db, base), audit.ErrInvalidEvent)
	foreign := testutil.CreateTestOrganization(t, f.db)
	foreignRole := testutil.CreateAgentRole(t, f.db, foreign.ID)
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", u.ID, f.org.ID).Update("role_id", foreignRole.ID).Error)
	require.ErrorIs(t, audit.RecordSecurity(f.db, base), audit.ErrInvalidEvent)
	require.Empty(t, f.securityRows(t))
}
