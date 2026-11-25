package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxLiveDeliveryScopesCurrentVisibilityAndRevocation(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	c := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	require.NoError(t, f.db.Exec("UPDATE inbox_cases SET assignee_id=?,team_id=? WHERE id=?", f.agent.ID, f.team.ID, c.ID).Error)
	accountB := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	b := c
	b.BaseModel, b.AccountID, b.AssigneeID = models.BaseModel{ID: uuid.New()}, accountB.ID, &f.admin.ID
	require.NoError(t, f.db.Create(&b).Error)
	auth := f.a.inboxLiveAuthorizer(f.org.ID, f.agent.ID, nil)
	scope := inboxlive.WatchScope{Kind: "queue", AccountID: &f.account.ID}
	event := inboxlive.Event{Protocol: 1, OrganizationID: f.org.ID, Kind: "case", ResourceID: c.ID, Version: 1,
		Before: &inboxlive.Scope{AccountID: &f.account.ID, ContactID: &c.ContactID, TeamID: &f.team.ID, AssigneeID: &f.agent.ID},
		After:  &inboxlive.Scope{AccountID: &f.account.ID, ContactID: &c.ContactID, TeamID: &f.team.ID, AssigneeID: &f.agent.ID}}
	decision, err := auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.True(t, decision.Allowed && decision.Match)
	require.False(t, decision.Reset)
	require.NotEmpty(t, decision.Authority)
	originalAuthority := decision.Authority
	other := event
	other.ResourceID, other.Before, other.After = b.ID, nil, &inboxlive.Scope{AccountID: &accountB.ID, ContactID: &c.ContactID, AssigneeID: &f.admin.ID}
	decision, err = auth(context.Background(), inboxlive.WatchScope{Kind: "queue"}, &other)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.False(t, decision.Match, "same contact on another account never grants delivery")
	other.OrganizationID = uuid.New()
	decision, err = auth(context.Background(), scope, &other)
	require.NoError(t, err)
	require.False(t, decision.Match)
	// The old queue is invalidated without exposing the newly inaccessible case.
	require.NoError(t, f.db.Exec("UPDATE inbox_cases SET assignee_id=? WHERE id=?", f.admin.ID, c.ID).Error)
	event.After.AssigneeID = &f.admin.ID
	decision, err = auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.True(t, decision.Allowed && decision.Match && decision.Reset)
	require.True(t, decision.Clear, "lost assignments clear old private queue rows")
	decision, err = auth(context.Background(), inboxlive.WatchScope{Kind: "case", CaseID: &c.ID}, &event)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	// Permission changes are noticed even for a broad configuration hint.
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "changed-live-role", []string{"chat:read", "contacts:read"})
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Update("role_id", role.ID).Error)
	event.Kind = "configuration"
	decision, err = auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.True(t, decision.Allowed && decision.Match && decision.Reset)
	require.NotEqual(t, originalAuthority, decision.Authority)
	require.NoError(t, f.db.Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Delete(&models.UserOrganization{}).Error)
	decision, err = auth(context.Background(), scope, &event)
	require.NoError(t, err, "confirmed revocation is distinct from unavailable storage")
	require.False(t, decision.Allowed)
}

func TestInboxLiveDeliveryWorkloadRetainsSelfOnlyAndAccountBoundaries(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	c := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	require.NoError(t, f.db.Exec("UPDATE inbox_cases SET assignee_id=? WHERE id=?", f.agent.ID, c.ID).Error)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "live-self-workload", []string{"chat:read", "analytics.agents:read"})
	// Use the model constant: the permission name is not a client-side grant.
	permission := models.Permission{Resource: models.ResourceAnalyticsAgents, Action: models.ActionRead}
	require.NoError(t, f.db.Where("resource=? AND action=?", permission.Resource, permission.Action).FirstOrCreate(&permission).Error)
	require.NoError(t, f.db.Exec("INSERT INTO role_permissions (custom_role_id,permission_id) VALUES (?,?) ON CONFLICT DO NOTHING", role.ID, permission.ID).Error)
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Update("role_id", role.ID).Error)
	auth := f.a.inboxLiveAuthorizer(f.org.ID, f.agent.ID, nil)
	scope := inboxlive.WatchScope{Kind: "workload", AccountID: &f.account.ID, TeamID: &f.team.ID}
	event := inboxlive.Event{Protocol: 1, OrganizationID: f.org.ID, Kind: "workload", ResourceID: f.agent.ID, Version: 1,
		After: &inboxlive.Scope{AccountID: &f.account.ID, TeamID: &f.team.ID, AssigneeID: &f.agent.ID}}
	decision, err := auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.True(t, decision.Allowed && decision.Match && decision.Reset)
	event.After.AssigneeID = &f.admin.ID
	decision, err = auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.False(t, decision.Match, "analytics alone does not grant other agents' workload")
	event.After.AssigneeID, event.After.AccountID = &f.agent.ID, nil
	decision, err = auth(context.Background(), inboxlive.WatchScope{Kind: "workload"}, &event)
	require.NoError(t, err)
	require.False(t, decision.Match, "unknown account is reconciled by polling, never expanded")
	accountB := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	event.After.AccountID = &accountB.ID
	decision, err = auth(context.Background(), inboxlive.WatchScope{Kind: "workload"}, &event)
	require.NoError(t, err)
	require.False(t, decision.Match)
}

func TestInboxLiveDeliveryCapacityIncludesCrossTeamLoadsWithoutGrantingCases(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	c := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	require.NoError(t, f.db.Exec("UPDATE inbox_cases SET assignee_id=?,team_id=? WHERE id=?", f.agent.ID, f.team.ID, c.ID).Error)
	second := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Synthetic other team", IsActive: true}
	require.NoError(t, f.db.Create(&second).Error)
	require.NoError(t, f.db.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: second.ID, UserID: f.agent.ID}).Error)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "live-capacity-reader", []string{"teams:read"})
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Update("role_id", role.ID).Error)
	auth := f.a.inboxLiveAuthorizer(f.org.ID, f.agent.ID, nil)
	scope := inboxlive.WatchScope{Kind: "capacity", AccountID: &f.account.ID, TeamID: &second.ID}
	event := inboxlive.Event{OrganizationID: f.org.ID, Kind: "case", ResourceID: c.ID, Version: 1}
	decision, err := auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.True(t, decision.Allowed && decision.Match && decision.Reset, "the same agent's capacity includes work assigned through another team")
	decision, err = auth(context.Background(), inboxlive.WatchScope{Kind: "case", CaseID: &c.ID}, &event)
	require.NoError(t, err)
	require.False(t, decision.Allowed, "aggregate team-read does not grant a conversation")
	event.Kind = "workload"
	event.After = &inboxlive.Scope{AccountID: &f.account.ID, AssigneeID: &f.agent.ID}
	decision, err = auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.True(t, decision.Allowed && decision.Match && decision.Reset, "account-level agent limits have no team ID but affect member teams")
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	event.After.AccountID = &otherAccount.ID
	decision, err = auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.False(t, decision.Match)
	event.After.AccountID, event.After.AssigneeID = &f.account.ID, &f.admin.ID
	decision, err = auth(context.Background(), scope, &event)
	require.NoError(t, err)
	require.False(t, decision.Match, "a nonmember's limits do not affect this team")
}
