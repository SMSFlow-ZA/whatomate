package handlers_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	extensions "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

// This helper issues no claims itself. Login, enrollment, TOTP confirmation and
// single-use recovery challenge all use the real host security handlers; actor
// proof is then obtained from the resulting signed session against Redis/SQL.
func emergencyCaseSession(t *testing.T, app *handlers.App, org uuid.UUID, name string, permissions []string) (*models.User, string, string, extensions.Actor) {
	t.Helper()
	var perms []models.Permission
	for _, p := range permissions {
		pair := strings.SplitN(p, ":", 2)
		record := models.Permission{Resource: pair[0], Action: pair[1]}
		require.NoError(t, app.DB.Where("resource=? AND action=?", record.Resource, record.Action).FirstOrCreate(&record).Error)
		perms = append(perms, record)
	}
	role := testutil.CreateTestRole(t, app.DB, org, name, perms)
	user := testutil.CreateTestUser(t, app.DB, org, testutil.WithEmail(testutil.UniqueEmail(name)), testutil.WithPassword("synthetic-emergency-password123"))
	require.NoError(t, app.DB.Model(user).Update("role_id", role.ID).Error)
	login := testutil.NewJSONRequest(t, map[string]string{"email": user.Email, "password": "synthetic-emergency-password123"})
	require.NoError(t, app.Login(login))
	require.Equal(t, 200, testutil.GetResponseStatusCode(login))
	primary := testutil.GetResponseCookie(login, "whm_access")
	require.NotEmpty(t, primary)
	begin := extensionRequest(t, app.Config.JWT.Secret, primary, map[string]any{})
	require.NoError(t, app.ExtensionMFABegin(begin))
	require.Equal(t, 200, testutil.GetResponseStatusCode(begin))
	var enrollment struct{ Data struct{ ID, Secret string } }
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(begin), &enrollment))
	require.NotEmpty(t, enrollment.Data.Secret)
	seed, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Data.Secret)
	require.NoError(t, e)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	h := hmac.New(sha1.New, seed)
	_, e = h.Write(counter[:])
	require.NoError(t, e)
	digest := h.Sum(nil)
	offset := digest[len(digest)-1] & 15
	code := fmt.Sprintf("%06d", (binary.BigEndian.Uint32(digest[offset:offset+4])&0x7fffffff)%1000000)
	confirm := extensionRequest(t, app.Config.JWT.Secret, primary, map[string]any{"enrollment_id": enrollment.Data.ID, "code": code})
	require.NoError(t, app.ExtensionMFAConfirm(confirm))
	require.Equal(t, 200, testutil.GetResponseStatusCode(confirm))
	var activation struct {
		Data struct {
			RecoveryCodes []string `json:"recovery_codes"`
		}
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(confirm), &activation))
	require.NotEmpty(t, activation.Data.RecoveryCodes)
	challenge := extensionRequest(t, app.Config.JWT.Secret, primary, map[string]any{"code": activation.Data.RecoveryCodes[0], "recovery": true})
	require.NoError(t, app.ExtensionMFAChallenge(challenge))
	require.Equal(t, 200, testutil.GetResponseStatusCode(challenge))
	verified := testutil.GetResponseCookie(challenge, "whm_access")
	actor, e := app.ExtensionActor(extensionRequest(t, app.Config.JWT.Secret, verified, nil))
	require.NoError(t, e)
	require.Equal(t, user.ID.String(), actor.UserID)
	require.True(t, actor.MFA)
	require.NotZero(t, actor.MFAGeneration)
	return user, primary, verified, actor
}

func TestHostEmergencyCaseReadUsesIndependentMFAApprovalAndProtectedBuffer(t *testing.T) {
	app := newTestApp(t)
	app.Config.Extensions = config.ExtensionsConfig{SecurityKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Modules: "directory,cases", RosterFreshnessSeconds: 86400}
	require.NoError(t, app.InitializeExtensions())
	org := testutil.CreateTestOrganization(t, app.DB)
	operator, primary, verified, operatorActor := emergencyCaseSession(t, app, org.ID, "emergency-operator", []string{"security:mfa.enroll", "security:breakglass.request", "security:breakglass.use", "cases:read.triage", "cases:assign.triage"})
	approver, _, _, approverActor := emergencyCaseSession(t, app, org.ID, "emergency-approver", []string{"security:mfa.enroll", "security:breakglass.approve", "security:breakglass.revoke"})
	require.NotEqual(t, operator.ID, approver.ID)
	ctx := context.Background()
	require.NoError(t, app.ExtensionAccess.Replace(ctx, org.ID.String(), operator.ID.String(), []addonapi.Grant{{Permission: "cases:read.triage", UnitID: "unit"}, {Permission: "cases:assign.triage", UnitID: "unit"}}))
	raw, e := app.DB.DB()
	require.NoError(t, e)
	service := cases.Service{Repository: &cases.SQLRepository{DB: raw}}
	triage := cases.Principal{ID: operator.ID.String(), Grants: []cases.Grant{{OrganizationID: org.ID.String(), UnitID: "unit", Permission: cases.AssignTriage}}}
	worker := cases.Principal{ID: "different-case-worker", Grants: []cases.Grant{{OrganizationID: org.ID.String(), UnitID: "unit", Permission: cases.WorkAssigned}}}
	makeCase := func(key string) cases.Case {
		created, e := service.Intake(ctx, cases.IntakeRequest{OrganizationID: org.ID.String(), UnitID: "unit", ContactID: "synthetic-employee-" + key, Key: key, Narrative: "HOST EMERGENCY PRIVATE MARKER " + key, Snapshot: cases.Classification{Zone: "unit", Department: "Synthetic", CaseType: "lateness"}})
		require.NoError(t, e)
		assigned, e := service.Assign(ctx, triage, created.CaseID, 1, []cases.Candidate{{Principal: worker, Role: cases.Handler}}, time.Now())
		require.NoError(t, e)
		return assigned
	}
	first, second := makeCase("first"), makeCase("second")
	request := func(token, method, id string, body any) (int, string) {
		r := extensionRequest(t, app.Config.JWT.Secret, token, body)
		r.RequestCtx.Request.Header.SetMethod(method)
		r.RequestCtx.Request.SetRequestURI("/api/extensions/cases/" + id)
		require.NoError(t, app.ExtensionDomainHandler(r))
		return testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r))
	}
	status, body := request(primary, http.MethodGet, first.ID, nil)
	require.Equal(t, 403, status)
	require.NotContains(t, body, "HOST EMERGENCY PRIVATE MARKER")
	status, body = request(verified, http.MethodGet, first.ID, nil)
	require.Equal(t, 404, status)
	require.NotContains(t, body, "HOST EMERGENCY PRIVATE MARKER")
	approve := func(caseID string) (string, uint64) {
		pending, e := app.ExtensionSecurity.RequestBreakGlass(ctx, operatorActor, caseID, "cases:read", "synthetic independently reviewed emergency", 5*time.Minute)
		require.NoError(t, e)
		approved, e := app.ExtensionSecurity.ApproveBreakGlass(ctx, approverActor, pending.ID(), pending.Version())
		require.NoError(t, e)
		require.Equal(t, approverActor.UserID, approved.ApprovedBy())
		return approved.ID(), approved.Version()
	}
	grant, version := approve(first.ID)
	var before []byte
	require.NoError(t, raw.QueryRowContext(ctx, "SELECT payload FROM addon_cases_case WHERE id=$1", first.ID).Scan(&before))
	status, body = request(verified, http.MethodPost, first.ID+"/emergency-read", map[string]any{"approval_id": grant})
	require.Equal(t, 200, status)
	require.Contains(t, body, "HOST EMERGENCY PRIVATE MARKER first")
	var after []byte
	require.NoError(t, raw.QueryRowContext(ctx, "SELECT payload FROM addon_cases_case WHERE id=$1", first.ID).Scan(&after))
	require.Equal(t, before, after, "read must not mutate assignments, version, case events or lifecycle")
	status, body = request(verified, http.MethodPost, second.ID+"/emergency-read", map[string]any{"approval_id": grant})
	require.Equal(t, 404, status)
	require.NotContains(t, body, "HOST EMERGENCY PRIVATE MARKER")
	status, body = request(verified, http.MethodPost, first.ID+"/emergency-read", map[string]any{"approval_id": "wrong-durable-approval"})
	require.Equal(t, 404, status)
	require.NotContains(t, body, "HOST EMERGENCY PRIVATE MARKER")
	_, e = app.ExtensionSecurity.RevokeBreakGlass(ctx, approverActor, grant, version)
	require.NoError(t, e)
	status, body = request(verified, http.MethodPost, first.ID+"/emergency-read", map[string]any{"approval_id": grant})
	require.Equal(t, 404, status)
	require.NotContains(t, body, "HOST EMERGENCY PRIVATE MARKER")
	subjectGrant, _ := approve(first.ID)
	require.NoError(t, app.ExtensionAccess.LinkIdentity(ctx, org.ID.String(), operator.ID.String(), "verified-roster-subject"))
	_, e = service.LinkParticipant(ctx, worker, first.ID, first.Version, cases.Principal{ID: "verified-roster-subject"}, cases.Subject, "verified subject identity for conflict acceptance", time.Now())
	require.NoError(t, e)
	status, body = request(verified, http.MethodPost, first.ID+"/emergency-read", map[string]any{"approval_id": subjectGrant})
	require.Equal(t, 404, status)
	require.NotContains(t, body, "HOST EMERGENCY PRIVATE MARKER")
	// A successful emergency response is first buffered by the real domain bridge.
	// Revoking its original session before that buffer leaves must suppress it.
	bufferGrant, _ := approve(second.ID)
	original := app.ExtensionAPI["cases"]
	app.ExtensionAPI["cases"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner := httptest.NewRecorder()
		original.ServeHTTP(inner, r)
		require.Equal(t, 200, inner.Code, "the inner read must succeed before in-flight revocation")
		require.Contains(t, inner.Body.String(), "HOST EMERGENCY PRIVATE MARKER second")
		for key, values := range inner.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(inner.Code)
		_, err := w.Write(inner.Body.Bytes())
		require.NoError(t, err)
		require.NoError(t, middleware.RevokeUserSessions(r.Context(), app.Redis, operator.ID))
	})
	status, body = request(verified, http.MethodPost, second.ID+"/emergency-read", map[string]any{"approval_id": bufferGrant})
	require.Equal(t, 403, status)
	require.NotContains(t, body, "HOST EMERGENCY PRIVATE MARKER")
}
