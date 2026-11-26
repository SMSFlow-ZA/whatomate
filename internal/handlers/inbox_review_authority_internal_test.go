package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxReviewAuthorityPreservesValidatedOrganisationSelection(t *testing.T) {
	f := newResponsePolicyFixture(t)
	other := testutil.CreateTestOrganization(t, f.db)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "home", []string{"chat:read"})
	member := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	selectedRole := testutil.CreateTestRoleWithKeys(t, f.db, other.ID, "selected", []string{"chat:read", "contacts:read", "teams:read"})
	membership := models.UserOrganization{UserID: member.ID, OrganizationID: other.ID, RoleID: &selectedRole.ID}
	require.NoError(t, f.db.Create(&membership).Error)
	require.NoError(t, f.db.Model(other).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, f.db, other.ID)
	contact := testutil.CreateTestContact(t, f.db, other.ID)
	c := newInboxCase(inboxThread{other.ID, account.ID, contact.ID}, time.Now().UTC(), &member.ID, nil)
	require.NoError(t, f.db.Create(&c).Error)
	check := func(person *models.User, forged bool) bool {
		r := f.request(t, nil, uuid.Nil)
		testutil.SetAuthContext(r, f.org.ID, person.ID)
		r.RequestCtx.Request.Header.Set("X-Organization-ID", other.ID.String())
		claims := &middleware.JWTClaims{UserID: person.ID, OrganizationID: f.org.ID, AuthStamp: person.AuthStamp, SessionID: "synthetic-bound-session"}
		var current models.User
		require.NoError(t, f.db.Select("auth_stamp").First(&current, "id=?", person.ID).Error)
		claims.AuthStamp = current.AuthStamp
		if forged {
			claims.OrganizationID = other.ID
		}
		r.RequestCtx.SetUserValue(middleware.ContextKeyClaims, claims)
		viewer, ok := f.a.freshInboxAuthority(r, models.ResourceChat, models.ActionRead)
		if ok {
			require.Equal(t, other.ID, viewer.Org)
			require.Equal(t, f.org.ID, claims.OrganizationID, "request claims must remain immutable")
			require.NoError(t, f.a.ListInboxRoutingPolicies(r))
			require.Equal(t, 200, testutil.GetResponseStatusCode(r), "selected-org routing retains current authority")
			testutil.SetPathParam(r, "account_id", account.ID.String())
			testutil.SetPathParam(r, "contact_id", contact.ID.String())
			testutil.SetPathParam(r, "id", contact.ID.String())
			require.NoError(t, f.a.GetInboxIntakeSummary(r))
			require.Equal(t, 200, testutil.GetResponseStatusCode(r), "selected-org intake summary retains current authority")
		}
		return ok
	}
	require.True(t, check(member, false), "a current second-org membership is supported")
	require.True(t, check(f.admin, false), "current support/super authority is supported")
	require.False(t, check(member, true), "a forged original identity is rejected")
	require.NoError(t, f.db.Delete(&membership).Error)
	require.False(t, check(member, false), "a removed override cannot mutate the home organisation")
}

// A missed cache invalidation must not turn an ordinary HTTP request into a
// requirement to connect a live socket before a revoked permission takes effect.
func TestInboxReviewPreviewRejectsRevokedContactPermissionWithWarmCache(t *testing.T) {
	f := newResponsePolicyFixture(t)
	f.a.Redis = testutil.SetupTestRedis(t)
	if f.a.Redis == nil {
		t.Skip("TEST_REDIS_URL required")
	}
	c := f.enter(t, models.InboxPriorityNormal, nil)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "review-preview", []string{"chat:read", "chat:write", "contacts:read"})
	actor := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	require.True(t, f.a.HasPermission(actor.ID, models.ResourceContacts, models.ActionRead, f.org.ID))
	require.NoError(t, f.db.Exec("DELETE FROM role_permissions rp USING permissions p WHERE rp.permission_id=p.id AND rp.custom_role_id=? AND p.resource=? AND p.action=?", role.ID, models.ResourceContacts, models.ActionRead).Error)
	r := f.request(t, map[string]any{"case_id": c.ID, "text": "Synthetic preview only"}, uuid.Nil)
	testutil.SetAuthContext(r, f.org.ID, actor.ID)
	require.NoError(t, f.a.PreviewInboxMessage(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r), "revoked full-contact read must not expose an unassigned case")
}

func TestInboxReviewPolicyRejectsRevokedSettingsWithWarmCache(t *testing.T) {
	f := newResponsePolicyFixture(t)
	f.a.Redis = testutil.SetupTestRedis(t)
	if f.a.Redis == nil {
		t.Skip("TEST_REDIS_URL required")
	}
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "review-policy", []string{"settings.general:read", "settings.general:write", "chat:read", "contacts:read"})
	actor := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	require.True(t, f.a.HasPermission(actor.ID, models.ResourceSettingsGeneral, models.ActionWrite, f.org.ID))
	require.NoError(t, f.db.Exec("DELETE FROM role_permissions rp USING permissions p WHERE rp.permission_id=p.id AND rp.custom_role_id=? AND p.resource=? AND p.action=?", role.ID, models.ResourceSettingsGeneral, models.ActionWrite).Error)
	r := f.request(t, responseInput("Synthetic policy", 30), uuid.Nil)
	testutil.SetAuthContext(r, f.org.ID, actor.ID)
	require.NoError(t, f.a.CreateInboxResponsePolicy(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r), "a stale cache cannot authorise policy creation")
	var count int64
	require.NoError(t, f.db.Model(&models.InboxResponsePolicy{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
}
