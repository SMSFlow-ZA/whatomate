package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxRoutingWorkflowPreviewAccountsForPriorOwnershipChanges(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	f.apply(t, models.AssignmentStrategyRoundRobin, 1)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "second-routing-agent", []string{"chat:read", "chat:write"})
	other := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.db.Create(&models.TeamMember{TeamID: f.team.ID, UserID: other.ID}).Error)
	c := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	w := models.InboxWorkflow{Kind: "macro", Actions: []models.InboxAction{
		{Type: "assign", Value: f.agent.ID.String()},
		{Type: "assign", Value: other.ID.String()},
		{Type: "assign", Value: f.agent.ID.String()},
	}}
	out, eligible, err := f.a.preflightInboxActions(f.db, w, c, f.admin.ID, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, "ready", out[0].Status)
	require.Equal(t, "ready", out[1].Status)
	require.Equal(t, "agent_rate_limited", out[2].Reason)
	var contact models.Contact
	require.NoError(t, f.db.First(&contact, "id=?", c.ContactID).Error)
	runtimeCase := c
	err = f.db.Transaction(func(tx *gorm.DB) error {
		return f.a.runInboxWorkflowActions(tx, &runtimeCase, &contact, f.admin.ID, w, time.Now().UTC())
	})
	require.ErrorIs(t, err, errInboxAssignmentRate)
	var count int64
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count, "failed multi-step assignment rolls back every receipt")
	w.Actions = []models.InboxAction{{Type: "assign", Value: f.agent.ID.String()}, {Type: "assign", Value: f.agent.ID.String()}}
	out, eligible, err = f.a.preflightInboxActions(f.db, w, c, f.admin.ID, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, eligible, "repeating the same owner consumes no additional slot")
	require.Equal(t, "ready", out[1].Status)
	w.Actions = append(w.Actions, models.InboxAction{Type: "assign_team", Value: f.team.ID.String()})
	out, _, err = f.a.preflightInboxActions(f.db, w, c, f.admin.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "deferred", out[2].Status)
	require.Equal(t, "routing_rechecks_prior_assignments", out[2].Reason)
}

func TestInboxRoutingConcurrentWorkersKeepOldestPriorityAndOneRateClaim(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	f.apply(t, models.AssignmentStrategyRoundRobin, 1)
	cases := make([]models.InboxCase, 6)
	for i := range cases {
		cases[i] = f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	}
	errs := make(chan error, len(cases))
	var workers sync.WaitGroup
	for _, c := range cases {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- f.a.routeOneInboxCase(context.Background(), c)
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var assigned []models.InboxCase
	require.NoError(t, f.db.Where("organization_id=? AND assignee_id IS NOT NULL", f.org.ID).Find(&assigned).Error)
	require.Len(t, assigned, 1)
	require.Equal(t, cases[0].ID, assigned[0].ID)
	var receipts int64
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=?", f.org.ID).Count(&receipts).Error)
	require.EqualValues(t, 1, receipts)
}

func TestInboxRoutingRateSpansTeamsButNotOtherAccounts(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	f.apply(t, models.AssignmentStrategyRoundRobin, 1)
	first := f.route(t, f.waiting(t, f.account.ID, models.InboxPriorityNormal))
	require.NotNil(t, first.AssigneeID)
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Synthetic second routing team", IsActive: true, AssignmentStrategy: models.AssignmentStrategyRoundRobin}
	require.NoError(t, f.db.Create(&team).Error)
	require.NoError(t, f.db.Create(&models.TeamMember{TeamID: team.ID, UserID: f.agent.ID}).Error)
	// Freeing active capacity must not erase the distinct assignment-rate slot.
	require.NoError(t, f.db.Model(&first).Update("status", models.InboxCaseResolved).Error)
	held := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	held.TeamID = &team.ID
	require.NoError(t, f.db.Save(&held).Error)
	held = f.route(t, held)
	require.Nil(t, held.AssigneeID)
	require.Equal(t, "rate_limited", held.RoutingReason)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	independent := f.waiting(t, otherAccount.ID, models.InboxPriorityNormal)
	independent.TeamID = &team.ID
	require.NoError(t, f.db.Save(&independent).Error)
	independent = f.route(t, independent)
	require.NotNil(t, independent.AssigneeID)
	require.Equal(t, f.agent.ID, *independent.AssigneeID)
	var receipts int64
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=? AND account_id=?", f.org.ID, f.account.ID).Count(&receipts).Error)
	require.EqualValues(t, 1, receipts)
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=? AND account_id=?", f.org.ID, otherAccount.ID).Count(&receipts).Error)
	require.EqualValues(t, 1, receipts)
}

func (f routingPolicyFixture) enable(t *testing.T) {
	t.Helper()
	require.NoError(t, database.InstallInboxAssignmentReceipts(f.db))
	require.NoError(t, f.db.Model(f.org).Update("inbox_workflows_enabled", true).Error)
}

func (f routingPolicyFixture) apply(t *testing.T, strategy models.AssignmentStrategy, maximum int) {
	t.Helper()
	seconds := 0
	if maximum > 0 {
		seconds = 3600
	}
	p := f.policy(t, inboxRoutingPolicyInput{Name: "Synthetic runtime policy", Strategy: strategy, MaxAssignments: maximum, WindowSeconds: seconds})
	status, input, _ := f.application(t, p, inboxRoutingApplication{AccountID: f.account.ID, TeamID: f.team.ID, AgentIDs: []uuid.UUID{f.agent.ID}, Revision: p.Revision}, false)
	require.Equal(t, 200, status)
	status, _, _ = f.application(t, p, input, true)
	require.Equal(t, 200, status)
}

func (f routingPolicyFixture) waiting(t *testing.T, account uuid.UUID, priority models.InboxCasePriority) models.InboxCase {
	t.Helper()
	c := routingRateCase(t, f.db, f.org.ID, account)
	c.TeamID, c.RoutingReason, c.Priority = &f.team.ID, "available_search", priority
	require.NoError(t, f.db.Save(&c).Error)
	return c
}

func (f routingPolicyFixture) route(t *testing.T, c models.InboxCase) models.InboxCase {
	t.Helper()
	require.NoError(t, f.a.routeOneInboxCase(context.Background(), c))
	var result models.InboxCase
	require.NoError(t, f.db.First(&result, "id=?", c.ID).Error)
	return result
}

func TestInboxRoutingNamedPolicyPrioritisesAndExplainsHeldWorkWithoutChangingOtherAccounts(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	require.NoError(t, f.db.Model(&f.team).Update("assignment_strategy", models.AssignmentStrategyManual).Error)
	f.apply(t, models.AssignmentStrategyRoundRobin, 1)
	older := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	urgent := f.waiting(t, f.account.ID, models.InboxPriorityUrgent)
	older = f.route(t, older)
	require.Nil(t, older.AssigneeID)
	require.Equal(t, "priority_wait", older.RoutingReason, "a concurrent lower-priority claim cannot jump the first due case")
	urgent = f.route(t, urgent)
	require.NotNil(t, urgent.AssigneeID)
	require.Equal(t, f.agent.ID, *urgent.AssigneeID)
	older = f.route(t, older)
	require.Nil(t, older.AssigneeID)
	require.Equal(t, "rate_limited", older.RoutingReason)
	// Returning availability can open a route without replacing the policy or
	// waiting for an unrelated agent's hour-long rate window to expire.
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "returning-routing-agent", []string{"chat:read", "chat:write"})
	returning := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.db.Create(&models.TeamMember{TeamID: f.team.ID, UserID: returning.ID}).Error)
	older = f.route(t, older)
	require.NotNil(t, older.AssigneeID)
	require.Equal(t, returning.ID, *older.AssigneeID)
	require.Empty(t, older.RoutingReason)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	other := f.waiting(t, otherAccount.ID, models.InboxPriorityUrgent)
	other = f.route(t, other)
	require.Nil(t, other.AssigneeID)
	require.Equal(t, "manual", other.RoutingReason, "the named override is scoped to the selected account only")
	var count int64
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=? AND account_id=?", f.org.ID, f.account.ID).Count(&count).Error)
	require.EqualValues(t, 2, count)
	older = f.route(t, older)
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=? AND account_id=?", f.org.ID, f.account.ID).Count(&count).Error)
	require.EqualValues(t, 2, count, "a repeated worker claim after assignment produces no second receipt")
	require.NoError(t, f.db.Model(&models.Message{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxRoutingNamedPolicyRechecksCurrentChatRightsAndAvailability(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	f.apply(t, models.InboxRoutingInherit, 0)
	readOnly := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "routing-read-only", []string{"chat:read"})
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Update("role_id", readOnly.ID).Error)
	c := f.waiting(t, f.account.ID, models.InboxPriorityHigh)
	c = f.route(t, c)
	require.Nil(t, c.AssigneeID)
	require.Equal(t, "permission_required", c.RoutingReason, "a native role must not replace the role of the current organisation membership")
	require.NoError(t, f.db.Model(f.agent).Update("is_available", false).Error)
	c = f.route(t, c)
	require.Equal(t, "unavailable", c.RoutingReason)
	writeRole := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "routing-restored-chat", []string{"chat:read", "chat:write"})
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Update("role_id", writeRole.ID).Error)
	require.NoError(t, f.db.Model(f.agent).Update("is_available", true).Error)
	c = f.route(t, c)
	require.NotNil(t, c.AssigneeID)
	require.Equal(t, f.agent.ID, *c.AssigneeID)
}

func TestInboxRoutingManualAndWorkflowPreflightHonourRateAndRecordAuthorisedOverride(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	f.apply(t, models.AssignmentStrategyRoundRobin, 1)
	first := f.waiting(t, f.account.ID, models.InboxPriorityUrgent)
	first = f.route(t, first)
	require.NotNil(t, first.AssigneeID)
	next := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	w := models.InboxWorkflow{Kind: "macro", Actions: []models.InboxAction{{Type: "assign", Value: f.agent.ID.String()}}}
	outcomes, eligible, err := f.a.preflightInboxActions(f.db, w, next, f.admin.ID, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, eligible)
	require.Equal(t, "agent_rate_limited", outcomes[0].Reason)
	teamWorkflow := w
	teamWorkflow.Actions = []models.InboxAction{{Type: "assign_team", Value: f.team.ID.String()}}
	outcomes, _, err = f.a.preflightInboxActions(f.db, teamWorkflow, next, f.admin.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "team_rate_limited", outcomes[0].Reason)
	assign := func(reason string) int {
		r := testutil.NewJSONRequest(t, map[string]any{"case_id": next.ID, "version": next.Version, "assignee_id": f.agent.ID, "override_reason": reason})
		testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
		testutil.SetPathParam(r, "account_id", f.account.ID.String())
		testutil.SetPathParam(r, "id", next.ContactID.String())
		require.NoError(t, f.a.AssignInboxConversation(r))
		return r.RequestCtx.Response.StatusCode()
	}
	require.Equal(t, 409, assign(""))
	unprivileged := testutil.CreateTestUser(t, f.db, f.org.ID)
	require.ErrorIs(t, f.db.Transaction(func(tx *gorm.DB) error {
		return assignInboxCase(tx, &next, unprivileged.ID, f.agent.ID, "Unapproved override")
	}), errInboxOverrideForbidden)
	require.Equal(t, 200, assign("Synthetic supervisor-reviewed urgency"))
	var activity models.InboxCaseActivity
	require.NoError(t, f.db.First(&activity, "case_id=? AND kind=?", next.ID, "assignment_rate_override").Error)
	require.Equal(t, "Synthetic supervisor-reviewed urgency", activity.Detail)
	var receipts int64
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=? AND account_id=? AND agent_id=?", f.org.ID, f.account.ID, f.agent.ID).Count(&receipts).Error)
	require.EqualValues(t, 2, receipts, "the real override counts; predictions and failed assignment do not")
}
