package handlers_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/extensions/security"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"net/http"
	"testing"
	"time"
)

// Caller-injected identity/body actor and absent security service must fail closed.
func TestExtensionActorRejectsMissingSignedSession(t *testing.T) {
	a := &handlers.App{}
	req := testutil.NewJSONRequest(t, map[string]any{"actor": map[string]any{"MFA": true, "Permissions": []string{"security:mfa.enroll"}}})
	_, err := a.ExtensionActor(req)
	require.Error(t, err)
	_, err = a.ExtensionActorFromClaims(context.Background(), &middleware.JWTClaims{})
	require.Error(t, err)
}

// Recovery authentication upgrades an ordinary primary session; refresh must retain
// the proof timestamp/generation and revocation must immediately invalidate it.
func TestExtensionMFAEnrollmentRecoveryRefreshAndRevocation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := []models.Permission{}
	for _, action := range []string{"mfa.enroll", "mfa.revoke"} {
		p := models.Permission{Resource: "security", Action: action}
		require.NoError(t, app.DB.Where("resource = ? AND action = ?", p.Resource, p.Action).FirstOrCreate(&p).Error)
		perms = append(perms, p)
	}
	role := testutil.CreateTestRole(t, app.DB, org.ID, "mfa-test", perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("mfa")), testutil.WithPassword("securepassword123"))
	require.NoError(t, app.DB.Model(user).Update("role_id", role.ID).Error)
	rawDB, err := app.DB.DB()
	require.NoError(t, err)
	app.ExtensionSecurity, err = security.New(rawDB, make([]byte, 32), security.Config{EnrollmentTTL: time.Minute, MaxBreakGlass: time.Minute})
	require.NoError(t, err)
	require.NoError(t, app.ExtensionSecurity.Migrate(context.Background()))
	login := testutil.NewJSONRequest(t, map[string]string{"email": user.Email, "password": "securepassword123"})
	require.NoError(t, app.Login(login))
	require.Equal(t, 200, testutil.GetResponseStatusCode(login))
	access := testutil.GetResponseCookie(login, "whm_access")
	req := extensionRequest(t, app.Config.JWT.Secret, access, map[string]any{})
	_, err = app.ExtensionActor(req)
	require.Error(t, err, "password alone cannot authorize protected data")
	require.NoError(t, app.ExtensionMFABegin(req))
	require.Equal(t, 200, testutil.GetResponseStatusCode(req))
	var begin struct {
		Data struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		}
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &begin))
	require.NotEmpty(t, begin.Data.Secret)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(begin.Data.Secret)
	require.NoError(t, err)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	h := hmac.New(sha1.New, seed)
	h.Write(counter[:])
	digest := h.Sum(nil)
	off := digest[len(digest)-1] & 15
	code := fmt.Sprintf("%06d", (binary.BigEndian.Uint32(digest[off:off+4])&0x7fffffff)%1000000)
	confirm := extensionRequest(t, app.Config.JWT.Secret, access, map[string]any{"enrollment_id": begin.Data.ID, "code": code})
	require.NoError(t, app.ExtensionMFAConfirm(confirm))
	require.Equal(t, 200, testutil.GetResponseStatusCode(confirm))
	var activation struct {
		Data struct {
			RecoveryCodes []string `json:"recovery_codes"`
		}
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(confirm), &activation))
	require.Len(t, activation.Data.RecoveryCodes, 10)
	challenge := extensionRequest(t, app.Config.JWT.Secret, access, map[string]any{"code": activation.Data.RecoveryCodes[0], "recovery": true})
	require.NoError(t, app.ExtensionMFAChallenge(challenge))
	require.Equal(t, 200, testutil.GetResponseStatusCode(challenge))
	verified := testutil.GetResponseCookie(challenge, "whm_access")
	actor, err := app.ExtensionActor(extensionRequest(t, app.Config.JWT.Secret, verified, nil))
	require.NoError(t, err)
	require.True(t, actor.MFA)
	refresh := testutil.NewJSONRequest(t, map[string]string{"refresh_token": testutil.GetResponseCookie(challenge, "whm_refresh")})
	require.NoError(t, app.RefreshToken(refresh))
	require.Equal(t, 200, testutil.GetResponseStatusCode(refresh))
	rotated := testutil.GetResponseCookie(refresh, "whm_access")
	before, err := middleware.ParseToken(app.Config.JWT.Secret, verified, "access")
	require.NoError(t, err)
	after, err := middleware.ParseToken(app.Config.JWT.Secret, rotated, "access")
	require.NoError(t, err)
	require.Equal(t, before.MFAAt, after.MFAAt)
	require.Equal(t, before.MFAGeneration, after.MFAGeneration)
	revoke := extensionRequest(t, app.Config.JWT.Secret, rotated, map[string]any{"generation": after.MFAGeneration})
	app.ExtensionAccess = &addonapi.AccessStore{DB: rawDB}
	require.NoError(t, app.ExtensionAccess.Migrate(context.Background()))
	require.NoError(t, app.ExtensionAccess.Replace(context.Background(), org.ID.String(), user.ID.String(), []addonapi.Grant{{Permission: "security:mfa.enroll", UnitID: "u"}}))
	for _, change := range []string{"scope", "alias"} {
		app.ExtensionAPI = map[string]http.Handler{"cases": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("protected-authority-marker"))
			if change == "scope" {
				require.NoError(t, app.ExtensionAccess.Replace(r.Context(), org.ID.String(), user.ID.String(), nil))
			} else {
				require.NoError(t, app.ExtensionAccess.LinkIdentity(r.Context(), org.ID.String(), user.ID.String(), "new-subject-alias"))
			}
		})}
		request := extensionRequest(t, app.Config.JWT.Secret, rotated, nil)
		request.RequestCtx.Request.SetRequestURI("/api/extensions/cases/test")
		require.NoError(t, app.ExtensionDomainHandler(request))
		require.Equal(t, 403, testutil.GetResponseStatusCode(request), change)
		require.NotContains(t, string(testutil.GetResponseBody(request)), "protected-authority-marker", change)
	}
	app.ExtensionAPI = map[string]http.Handler{"cases": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate revocation while a domain operation has a response buffered.
		_, _ = w.Write([]byte("protected-response-marker"))
		require.NoError(t, app.ExtensionMFARevoke(revoke))
	})}
	domain := extensionRequest(t, app.Config.JWT.Secret, rotated, nil)
	domain.RequestCtx.Request.SetRequestURI("/api/extensions/cases/test")
	require.NoError(t, app.ExtensionDomainHandler(domain))
	require.Equal(t, 403, testutil.GetResponseStatusCode(domain))
	require.NotContains(t, string(testutil.GetResponseBody(domain)), "protected-response-marker")
	require.Equal(t, 200, testutil.GetResponseStatusCode(revoke))
	_, err = app.ExtensionActor(extensionRequest(t, app.Config.JWT.Secret, rotated, nil))
	require.Error(t, err)
}

func extensionRequest(t *testing.T, secret, token string, body any) *fastglue.Request {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	claims, err := middleware.ParseToken(secret, token, "access")
	require.NoError(t, err)
	req.RequestCtx.SetUserValue(middleware.ContextKeyClaims, claims)
	req.RequestCtx.SetUserValue("user_id", claims.UserID)
	req.RequestCtx.SetUserValue("organization_id", claims.OrganizationID)
	return req
}

func TestExtensionEmergencyRoutesRejectBodyActorsWithoutSignedSession(t *testing.T) {
	app := &handlers.App{}
	for _, handler := range []func(*fastglue.Request) error{app.ExtensionEmergencyRequest, app.ExtensionEmergencyApprove, app.ExtensionEmergencyUse, app.ExtensionEmergencyRevoke} {
		req := testutil.NewJSONRequest(t, map[string]any{"actor": map[string]any{"MFA": true, "Permissions": []string{"security:breakglass.approve"}}, "id": "grant", "version": 1})
		require.NoError(t, handler(req))
		require.Equal(t, 403, testutil.GetResponseStatusCode(req))
	}
}
