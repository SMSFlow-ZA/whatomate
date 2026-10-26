package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/extensions/security"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func primaryDemoFixture(t *testing.T) (*handlers.App, *middleware.JWTClaims, string, string) {
	t.Helper()
	a := newTestApp(t)
	org := testutil.CreateTestOrganization(t, a.DB)
	for _, key := range []string{"cases:read.own", "knowledge:read", "security:mfa.revoke"} {
		pair := strings.SplitN(key, ":", 2)
		permission := models.Permission{Resource: pair[0], Action: pair[1]}
		require.NoError(t, a.DB.Where("resource = ? AND action = ?", pair[0], pair[1]).FirstOrCreate(&permission).Error)
	}
	role := testutil.CreateTestRoleWithKeys(t, a.DB, org.ID, "primary-demo", []string{"cases:read.own", "knowledge:read", "security:mfa.revoke"})
	user := testutil.CreateTestUser(t, a.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("primary-demo")), testutil.WithPassword("securepassword123"))
	require.NoError(t, a.DB.Model(user).Update("role_id", role.ID).Error)
	db, e := a.DB.DB()
	require.NoError(t, e)
	a.ExtensionSecurity, e = security.New(db, make([]byte, 32), security.Config{EnrollmentTTL: time.Minute, MaxBreakGlass: time.Minute})
	require.NoError(t, e)
	require.NoError(t, a.ExtensionSecurity.Migrate(context.Background()))
	a.ExtensionAccess = &addonapi.AccessStore{DB: db}
	require.NoError(t, a.ExtensionAccess.Migrate(context.Background()))
	require.NoError(t, a.ExtensionAccess.Replace(context.Background(), org.ID.String(), user.ID.String(), []addonapi.Grant{{Permission: "cases:read.own", UnitID: "own-unit"}, {Permission: "knowledge:read", Audience: "staff"}}))
	login := testutil.NewJSONRequest(t, map[string]string{"email": user.Email, "password": "securepassword123"})
	require.NoError(t, a.Login(login))
	require.Equal(t, 200, testutil.GetResponseStatusCode(login))
	access := testutil.GetResponseCookie(login, "whm_access")
	claims, e := middleware.ParseToken(a.Config.JWT.Secret, access, "access")
	require.NoError(t, e)
	return a, claims, access, testutil.GetResponseCookie(login, "whm_refresh")
}

func TestPrimarySessionDemoTrustedBridgeAndTruthfulStatus(t *testing.T) {
	a, claims, access, _ := primaryDemoFixture(t)
	ctx := context.Background()
	_, e := a.ExtensionActorFromClaims(ctx, claims)
	require.Error(t, e, "default requires actual MFA")
	a.Config.Extensions.PrimarySessionDemoOrganizationID = claims.OrganizationID.String()
	actor, e := a.ExtensionActorFromClaims(ctx, claims)
	require.NoError(t, e)
	require.True(t, actor.PrimarySessionAuthorized)
	require.False(t, actor.MFA)
	require.Zero(t, actor.MFAGeneration)
	require.ElementsMatch(t, []string{"cases:read.own", "knowledge:read", "security:mfa.revoke"}, actor.Permissions)
	a.ExtensionRegistry = ext.NewRegistry()
	catalog := extensionRequest(t, a.Config.JWT.Secret, access, nil)
	require.NoError(t, a.ExtensionCatalog(catalog))
	require.Equal(t, 200, testutil.GetResponseStatusCode(catalog))
	var response struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(catalog), &response))
	require.Equal(t, false, response.Data["mfa_verified"])
	require.Equal(t, true, response.Data["primary_session_authorized"])
	require.Equal(t, "primary-session-demo", response.Data["authentication_policy"])
	require.Equal(t, a.GenericOrganizationAllowed(ctx, claims.OrganizationID), response.Data["generic_host_access"])
	status := extensionRequest(t, a.Config.JWT.Secret, access, nil)
	require.NoError(t, a.ExtensionMFAStatus(status))
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(status), &response))
	require.Equal(t, false, response.Data["required"])
	require.Equal(t, false, response.Data["verified"])
	require.Equal(t, false, response.Data["enabled"])
	require.Equal(t, float64(0), response.Data["generation"])
	revoke := extensionRequest(t, a.Config.JWT.Secret, access, map[string]any{"generation": 0})
	require.NoError(t, a.ExtensionMFARevoke(revoke))
	require.Equal(t, 403, testutil.GetResponseStatusCode(revoke), "demo policy cannot revoke factors")
	// Actual domain bridge preserves the policy bit and still intersects exact
	// live role permissions with unit/audience scope; body JSON cannot grant it.
	a.ExtensionAPI = map[string]http.Handler{"cases": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current, e := addonapi.ActorFrom(r.Context())
		require.NoError(t, e)
		require.True(t, current.PrimarySessionAuthorized)
		require.False(t, current.MFA)
		require.True(t, current.Allows("cases:read.own", "own-unit"))
		require.False(t, current.Allows("cases:read.own", "other-unit"))
		require.True(t, current.AudienceAllowed("knowledge:read", "staff"))
		require.False(t, current.AudienceAllowed("knowledge:read", "other"))
		require.False(t, current.Has("cases:read.triage"))
		_, _ = w.Write([]byte("protected-marker"))
	})}
	request := extensionRequest(t, a.Config.JWT.Secret, access, nil)
	request.RequestCtx.Request.SetRequestURI("/api/extensions/cases/test")
	require.NoError(t, a.ExtensionDomainHandler(request))
	require.Equal(t, 200, testutil.GetResponseStatusCode(request))
	require.Contains(t, string(testutil.GetResponseBody(request)), "protected-marker")
	forged := testutil.NewJSONRequest(t, map[string]any{"primary_session_authorized": true, "MFA": true})
	_, e = a.ExtensionActor(forged)
	require.Error(t, e, "API key/body identity is not a signed session")
	stale := *claims
	stale.AuthStamp = "changed"
	_, e = a.ExtensionActorFromClaims(ctx, &stale)
	require.Error(t, e)
	wrongPurpose := *claims
	wrongPurpose.Subject = "refresh"
	_, e = a.ExtensionActorFromClaims(ctx, &wrongPurpose)
	require.Error(t, e)
	// A policy change while the response is buffered prevents protected delivery.
	a.ExtensionAPI["cases"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("buffered-marker"))
		a.Config.Extensions.PrimarySessionDemoOrganizationID = ""
	})
	request = extensionRequest(t, a.Config.JWT.Secret, access, nil)
	request.RequestCtx.Request.SetRequestURI("/api/extensions/cases/test")
	require.NoError(t, a.ExtensionDomainHandler(request))
	require.Equal(t, 403, testutil.GetResponseStatusCode(request))
	require.NotContains(t, string(testutil.GetResponseBody(request)), "buffered-marker")
	a.Config.Extensions.PrimarySessionDemoOrganizationID = uuid.NewString()
	_, e = a.ExtensionActorFromClaims(ctx, claims)
	require.Error(t, e, "another configured org cannot relax this session")
	a.Config.Extensions.PrimarySessionDemoOrganizationID = claims.OrganizationID.String()
	require.NoError(t, a.Redis.Del(ctx, middleware.SessionKey(claims.SessionID)).Err())
	_, e = a.ExtensionActorFromClaims(ctx, claims)
	require.Error(t, e, "revoked session stays revoked")
}

func TestPrimarySessionDemoMFAStorageFailureAndSocketRecheck(t *testing.T) {
	a, claims, access, _ := primaryDemoFixture(t)
	a.Config.Extensions.PrimarySessionDemoOrganizationID = claims.OrganizationID.String()
	ws := extensionRequest(t, a.Config.JWT.Secret, access, nil)
	require.NoError(t, a.GetWSToken(ws))
	require.Equal(t, 200, testutil.GetResponseStatusCode(ws))
	var response struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(ws), &response))
	socket, e := middleware.ParseToken(a.Config.JWT.Secret, response.Data.Token, "ws")
	require.NoError(t, e)
	actor, e := a.ExtensionActorFromClaims(context.Background(), socket)
	require.NoError(t, e)
	require.True(t, actor.PrimarySessionAuthorized)
	require.False(t, actor.MFA)
	a.Config.Extensions.PrimarySessionDemoOrganizationID = ""
	_, e = a.ExtensionActorFromClaims(context.Background(), socket)
	require.Error(t, e, "socket delivery rechecks current policy")
	// Closed disposable pool injects a real MFA storage failure without touching
	// the real enrollment rows. No zero generation is invented as factor proof.
	failed, e := sql.Open("pgx", os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, e)
	require.NoError(t, failed.Close())
	a.ExtensionSecurity, e = security.New(failed, make([]byte, 32), security.Config{EnrollmentTTL: time.Minute, MaxBreakGlass: time.Minute})
	require.NoError(t, e)
	proofClaims := *claims
	proofClaims.MFAAt = jwt.NewNumericDate(time.Now())
	proofClaims.MFAGeneration = 99
	_, e = a.ExtensionActorFromClaims(context.Background(), &proofClaims)
	require.Error(t, e, "production fails on MFA storage failure")
	a.Config.Extensions.PrimarySessionDemoOrganizationID = claims.OrganizationID.String()
	actor, e = a.ExtensionActorFromClaims(context.Background(), &proofClaims)
	require.NoError(t, e)
	require.True(t, actor.PrimarySessionAuthorized)
	require.False(t, actor.MFA)
	require.Zero(t, actor.MFAGeneration)
}

func TestPrimarySessionDemoRefreshKeepsFactorTruthAndDefaultGate(t *testing.T) {
	a, claims, _, refresh := primaryDemoFixture(t)
	old, e := middleware.ParseToken(a.Config.JWT.Secret, refresh, "refresh")
	require.NoError(t, e)
	old.MFAAt = jwt.NewNumericDate(time.Now().Add(-24 * time.Hour))
	old.MFAGeneration = 99
	stale, e := jwt.NewWithClaims(jwt.SigningMethodHS256, old).SignedString([]byte(a.Config.JWT.Secret))
	require.NoError(t, e)
	for _, configured := range []string{"", uuid.NewString()} {
		a.Config.Extensions.PrimarySessionDemoOrganizationID = configured
		r := testutil.NewJSONRequest(t, map[string]string{"refresh_token": stale})
		require.NoError(t, a.RefreshToken(r))
		require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	}
	a.Config.Extensions.PrimarySessionDemoOrganizationID = claims.OrganizationID.String()
	r := testutil.NewJSONRequest(t, map[string]string{"refresh_token": stale})
	require.NoError(t, a.RefreshToken(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	access := testutil.GetResponseCookie(r, "whm_access")
	rotated, e := middleware.ParseToken(a.Config.JWT.Secret, access, "access")
	require.NoError(t, e)
	require.Equal(t, old.MFAAt, rotated.MFAAt)
	require.Equal(t, old.MFAGeneration, rotated.MFAGeneration)
	actor, e := a.ExtensionActorFromClaims(context.Background(), rotated)
	require.NoError(t, e)
	require.False(t, actor.MFA)
	require.Zero(t, actor.MFAGeneration)
	require.True(t, actor.PrimarySessionAuthorized)
}

func TestPrimarySessionDemoAPIKeyCannotMintSessions(t *testing.T) {
	a, claims, _, _ := primaryDemoFixture(t)
	a.Config.Extensions.PrimarySessionDemoOrganizationID = claims.OrganizationID.String()
	for _, action := range []string{"ws", "switch"} {
		t.Run(action, func(t *testing.T) {
			// API-key authentication sets these identities but supplies no signed
			// access JWT claims. Such a request must never mint a primary session.
			r := testutil.NewJSONRequest(t, map[string]string{"organization_id": claims.OrganizationID.String()})
			testutil.SetAuthContext(r, claims.OrganizationID, claims.UserID)
			if action == "ws" {
				require.NoError(t, a.GetWSToken(r))
			} else {
				require.NoError(t, a.SwitchOrg(r))
			}
			require.Equal(t, 401, testutil.GetResponseStatusCode(r))
			require.Empty(t, testutil.GetResponseCookie(r, "whm_access"))
			require.Empty(t, testutil.GetResponseCookie(r, "whm_refresh"))
			require.NotContains(t, string(testutil.GetResponseBody(r)), `"token"`)
		})
	}
}

func TestPrimarySessionDemoHasNoSuperadminMembershipShortcut(t *testing.T) {
	a, claims, _, _ := primaryDemoFixture(t)
	other := testutil.CreateTestOrganization(t, a.DB)
	require.NoError(t, a.DB.Model(&models.User{}).Where("id = ?", claims.UserID).Updates(map[string]any{"is_super_admin": true, "role_id": nil}).Error)
	foreign := *claims
	foreign.OrganizationID = other.ID
	a.Config.Extensions.PrimarySessionDemoOrganizationID = other.ID.String()
	_, err := a.ExtensionActorFromClaims(context.Background(), &foreign)
	require.Error(t, err, "superadmin without membership must not construct a demo actor")
	a.Config.Extensions.PrimarySessionDemoOrganizationID = claims.OrganizationID.String()
	require.NoError(t, a.DB.Model(&models.User{}).Where("id = ?", claims.UserID).Update("is_active", false).Error)
	_, err = a.ExtensionActorFromClaims(context.Background(), claims)
	require.Error(t, err, "disabled primary identity stays disabled")
}
