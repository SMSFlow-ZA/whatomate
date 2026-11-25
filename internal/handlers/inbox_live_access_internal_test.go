package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxLiveSessionStorageOutageDoesNotClaimRevocation(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	f.a.Redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	t.Cleanup(func() { _ = f.a.Redis.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	claims := &middleware.JWTClaims{UserID: f.agent.ID, OrganizationID: f.org.ID, SessionID: "synthetic-session", SessionFamily: "synthetic-family"}
	decision, err := f.a.inboxLiveAuthorizer(f.org.ID, f.agent.ID, claims)(ctx, inboxlive.WatchScope{Kind: "queue"}, nil)
	require.ErrorIs(t, err, errAuditStorage)
	require.False(t, decision.Allowed)
}

func TestInboxLiveWatchAuthorityUsesCurrentRolesAndAccountScopedAssignments(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	c := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	c.AssigneeID = &f.agent.ID
	require.NoError(t, f.db.Save(&c).Error)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	otherCase := c
	otherCase.BaseModel, otherCase.AccountID, otherCase.AssigneeID = models.BaseModel{ID: uuid.New()}, otherAccount.ID, &f.admin.ID
	require.NoError(t, f.db.Create(&otherCase).Error)
	foreign := testutil.CreateTestOrganization(t, f.db)
	foreignAccount := testutil.CreateTestWhatsAppAccount(t, f.db, foreign.ID)
	viewer, err := f.a.inboxLiveViewer(context.Background(), f.org.ID, f.agent.ID, nil)
	require.NoError(t, err)
	for _, fixture := range []struct {
		scope inboxlive.WatchScope
		allow bool
	}{
		{inboxlive.WatchScope{Kind: "queue", AccountID: &f.account.ID}, true},
		{inboxlive.WatchScope{Kind: "queue", AccountID: &foreignAccount.ID}, false},
		{inboxlive.WatchScope{Kind: "case", CaseID: &c.ID}, true},
		{inboxlive.WatchScope{Kind: "case", CaseID: &otherCase.ID}, false},
		{inboxlive.WatchScope{Kind: "conversation", AccountID: &f.account.ID, ContactID: &c.ContactID}, true},
		{inboxlive.WatchScope{Kind: "conversation", AccountID: &otherAccount.ID, ContactID: &c.ContactID}, false},
		{inboxlive.WatchScope{Kind: "contact", ContactID: &c.ContactID}, false},
		{inboxlive.WatchScope{Kind: "capacity", AccountID: &f.account.ID, TeamID: &f.team.ID}, false},
		{inboxlive.WatchScope{Kind: "workload"}, false},
		{inboxlive.WatchScope{Kind: "supervisor"}, false},
	} {
		allowed, err := inboxLiveScopeAllowed(f.db, viewer, fixture.scope)
		require.NoError(t, err)
		require.Equal(t, fixture.allow, allowed, fixture.scope.Key())
	}
	// Explicit contact ownership retains the existing contact-level grant.
	require.NoError(t, f.db.Model(&models.Contact{}).Where("id=?", c.ContactID).Update("assigned_user_id", f.agent.ID).Error)
	allowed, err := inboxLiveScopeAllowed(f.db, viewer, inboxlive.WatchScope{Kind: "conversation", AccountID: &otherAccount.ID, ContactID: &c.ContactID})
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, f.db.Model(&models.Contact{}).Where("id=?", c.ContactID).Update("assigned_user_id", nil).Error)
	// Ownership revocation is evaluated again, rather than granted by the old
	// subscription or a former historical assignment.
	require.NoError(t, f.db.Exec("UPDATE inbox_cases SET assignee_id=? WHERE id=?", f.admin.ID, c.ID).Error)
	allowed, err = inboxLiveScopeAllowed(f.db, viewer, inboxlive.WatchScope{Kind: "case", CaseID: &c.ID})
	require.NoError(t, err)
	require.False(t, allowed)
	denied := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "live-denied", []string{"chat:write"})
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Update("role_id", denied.ID).Error)
	current, err := f.a.inboxLiveViewer(context.Background(), f.org.ID, f.agent.ID, nil)
	require.NoError(t, err)
	allowed, err = inboxLiveScopeAllowed(f.db, current, inboxlive.WatchScope{Kind: "queue"})
	require.NoError(t, err)
	require.False(t, allowed, "the native role cannot replace current membership permissions")
	require.NoError(t, f.db.Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Delete(&models.UserOrganization{}).Error)
	_, err = f.a.inboxLiveViewer(context.Background(), f.org.ID, f.agent.ID, nil)
	require.ErrorIs(t, err, errAuditForbidden)
}

func TestInboxLiveWatchAuthorityChecksViewerClaimsFeatureAndScopeOwnership(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	viewer, err := f.a.inboxLiveViewer(context.Background(), f.org.ID, f.admin.ID, nil)
	require.NoError(t, err)
	allowed, err := inboxLiveScopeAllowed(f.db, viewer, inboxlive.WatchScope{Kind: "capacity", AccountID: &f.account.ID, TeamID: &f.team.ID})
	require.NoError(t, err)
	require.True(t, allowed)
	foreign := testutil.CreateTestOrganization(t, f.db)
	foreignTeam := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: foreign.ID, Name: "Synthetic foreign team", IsActive: true}
	require.NoError(t, f.db.Create(&foreignTeam).Error)
	allowed, err = inboxLiveScopeAllowed(f.db, viewer, inboxlive.WatchScope{Kind: "queue", TeamID: &foreignTeam.ID})
	require.NoError(t, err)
	require.False(t, allowed)
	_, err = f.a.inboxLiveViewer(context.Background(), f.org.ID, f.agent.ID, &middleware.JWTClaims{UserID: f.admin.ID, OrganizationID: f.org.ID})
	require.ErrorIs(t, err, errAuditUnauthenticated)
	_, err = f.a.inboxLiveViewer(context.Background(), f.org.ID, f.agent.ID, &middleware.JWTClaims{UserID: f.agent.ID, OrganizationID: f.org.ID, SessionID: "revoked-test-session"})
	require.ErrorIs(t, err, errAuditUnauthenticated, "missing session storage cannot grant live access")
	require.NoError(t, f.db.Model(f.org).Update("inbox_workflows_enabled", false).Error)
	_, err = f.a.inboxLiveViewer(context.Background(), f.org.ID, f.admin.ID, nil)
	require.ErrorIs(t, err, errInboxDisabled)
}
