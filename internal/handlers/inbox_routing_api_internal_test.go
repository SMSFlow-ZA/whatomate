package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type routingPolicyFixture struct {
	db           *gorm.DB
	a            *App
	org          *models.Organization
	admin, agent *models.User
	account      *models.WhatsAppAccount
	team         models.Team
}

func newRoutingPolicyFixture(t *testing.T) routingPolicyFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "routing-agent", []string{"chat:read", "chat:write"})
	agent := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Synthetic routing team", IsActive: true, AssignmentStrategy: models.AssignmentStrategyRoundRobin}
	require.NoError(t, db.Create(&team).Error)
	require.NoError(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: agent.ID}).Error)
	return routingPolicyFixture{db: db, a: &App{DB: db, Log: testutil.NopLogger()}, org: org, admin: admin, agent: agent, account: account, team: team}
}

func (f routingPolicyFixture) policy(t *testing.T, input inboxRoutingPolicyInput) models.InboxRoutingPolicy {
	t.Helper()
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	require.NoError(t, f.a.SaveInboxRoutingPolicy(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
	var result struct{ Policy models.InboxRoutingPolicy }
	testutil.ParseEnvelopeResponse(t, r, &result)
	return result.Policy
}

func (f routingPolicyFixture) application(t *testing.T, policy models.InboxRoutingPolicy, input inboxRoutingApplication, apply bool) (int, inboxRoutingApplication, bool) {
	t.Helper()
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	testutil.SetPathParam(r, "policy_id", policy.ID.String())
	var err error
	if apply {
		err = f.a.ApplyInboxRoutingPolicy(r)
	} else {
		err = f.a.PreviewInboxRoutingPolicy(r)
	}
	require.NoError(t, err)
	var result struct {
		Confirmation   string    `json:"confirmation"`
		ExpiresAt      time.Time `json:"expires_at"`
		AlreadyApplied bool      `json:"already_applied"`
	}
	if r.RequestCtx.Response.StatusCode() == 200 {
		testutil.ParseEnvelopeResponse(t, r, &result)
	}
	input.Confirmation, input.ExpiresAt = result.Confirmation, result.ExpiresAt
	return r.RequestCtx.Response.StatusCode(), input, result.AlreadyApplied
}

func TestInboxRoutingPolicyPreviewCopiesSettingsOnlyAfterConfirmedApply(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	original := models.InboxAgentCapacity{OrganizationID: f.org.ID, AccountID: f.account.ID, AgentID: f.agent.ID, MaxActive: 7}
	require.NoError(t, f.db.Create(&original).Error)
	p := f.policy(t, inboxRoutingPolicyInput{Name: "Synthetic fair routing", Strategy: models.AssignmentStrategyLoadBalanced, MaxAssignments: 10, WindowSeconds: 3600})
	input := inboxRoutingApplication{AccountID: f.account.ID, TeamID: f.team.ID, AgentIDs: []uuid.UUID{f.agent.ID}, Revision: p.Revision}
	status, reviewed, _ := f.application(t, p, input, false)
	require.Equal(t, 200, status)
	require.Len(t, reviewed.Confirmation, 64)
	for _, model := range []any{&models.InboxRoutingConfiguration{}, &models.InboxAgentAssignmentRate{}, &models.InboxAssignmentReceipt{}} {
		var count int64
		require.NoError(t, f.db.Model(model).Where("organization_id=?", f.org.ID).Count(&count).Error)
		require.Zero(t, count, "preview must not modify settings or ownership")
	}
	status, _, replay := f.application(t, p, reviewed, true)
	require.Equal(t, 200, status)
	require.False(t, replay)
	var config models.InboxRoutingConfiguration
	require.NoError(t, f.db.First(&config, "organization_id=? AND account_id=? AND team_id=?", f.org.ID, f.account.ID, f.team.ID).Error)
	require.EqualValues(t, 1, config.Revision)
	require.Equal(t, models.AssignmentStrategyLoadBalanced, config.Strategy)
	var capacity models.InboxAgentCapacity
	require.NoError(t, f.db.First(&capacity, "organization_id=? AND account_id=? AND agent_id=?", f.org.ID, f.account.ID, f.agent.ID).Error)
	require.Equal(t, 7, capacity.MaxActive, "nil active limit preserves the deployed value")
	status, _, replay = f.application(t, p, reviewed, true)
	require.Equal(t, 200, status)
	require.True(t, replay, "an uncertain Apply retry returns the stored completion, not a second copy")
	var rate models.InboxAgentAssignmentRate
	require.NoError(t, f.db.First(&rate, "organization_id=? AND account_id=? AND agent_id=?", f.org.ID, f.account.ID, f.agent.ID).Error)
	require.EqualValues(t, 1, rate.Revision)
	require.Equal(t, 10, rate.MaxAssignments)
	// Source template edits and archiving never propagate into deployed limits.
	for i, body := range []inboxRoutingPolicyInput{
		{Revision: p.Revision, Name: "Edited source", Strategy: models.AssignmentStrategyManual, MaxAssignments: 1, WindowSeconds: 60},
		{Revision: p.Revision + 1, Archive: true},
	} {
		r := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
		testutil.SetPathParam(r, "policy_id", p.ID.String())
		require.NoError(t, f.a.SaveInboxRoutingPolicy(r))
		require.Equal(t, 200, r.RequestCtx.Response.StatusCode(), "template change %d", i)
	}
	rate = models.InboxAgentAssignmentRate{}
	require.NoError(t, f.db.First(&rate, "organization_id=? AND account_id=? AND agent_id=?", f.org.ID, f.account.ID, f.agent.ID).Error)
	require.Equal(t, 10, rate.MaxAssignments)
	config = models.InboxRoutingConfiguration{}
	require.NoError(t, f.db.First(&config, "organization_id=? AND account_id=? AND team_id=?", f.org.ID, f.account.ID, f.team.ID).Error)
	require.EqualValues(t, 1, config.PolicyRevision)
	require.Equal(t, p.Name, config.Name)
}

func TestInboxRoutingReviewReportsCurrentAndProposedStrategies(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	p := f.policy(t, inboxRoutingPolicyInput{Name: "Reviewed strategy change", Strategy: models.AssignmentStrategyManual})
	r := testutil.NewJSONRequest(t, inboxRoutingApplication{AccountID: f.account.ID, TeamID: f.team.ID, AgentIDs: []uuid.UUID{f.agent.ID}, Revision: p.Revision})
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	testutil.SetPathParam(r, "policy_id", p.ID.String())
	require.NoError(t, f.a.PreviewInboxRoutingPolicy(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	var result struct {
		Before *models.AssignmentStrategy `json:"before_strategy"`
		After  models.AssignmentStrategy  `json:"effective_strategy"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	require.NotNil(t, result.Before)
	require.Equal(t, models.AssignmentStrategyRoundRobin, *result.Before)
	require.Equal(t, models.AssignmentStrategyManual, result.After)
	var count int64
	require.NoError(t, f.db.Model(&models.InboxRoutingConfiguration{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxRoutingPolicyRejectsStaleExpiredAndCrossScopeApplications(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	maximum := 5
	p := f.policy(t, inboxRoutingPolicyInput{Name: "Scoped policy", Strategy: models.InboxRoutingInherit, MaxActive: &maximum})
	input := inboxRoutingApplication{AccountID: f.account.ID, TeamID: f.team.ID, AgentIDs: []uuid.UUID{f.agent.ID}, Revision: p.Revision}
	status, reviewed, _ := f.application(t, p, input, false)
	require.Equal(t, 200, status)
	expired := reviewed
	expired.ExpiresAt = time.Now().UTC().Add(-time.Second)
	status, _, _ = f.application(t, p, expired, true)
	require.Equal(t, 409, status)
	duplicate := input
	duplicate.AgentIDs = []uuid.UUID{f.agent.ID, f.agent.ID}
	status, _, _ = f.application(t, p, duplicate, false)
	require.Equal(t, 400, status)
	foreign := testutil.CreateTestOrganization(t, f.db)
	foreignAccount := testutil.CreateTestWhatsAppAccount(t, f.db, foreign.ID)
	wrong := reviewed
	wrong.AccountID = foreignAccount.ID
	status, _, _ = f.application(t, p, wrong, true)
	require.Equal(t, 404, status)
	// Changing a member/limit after review invalidates the copied settings.
	require.NoError(t, f.db.Create(&models.InboxAgentCapacity{OrganizationID: f.org.ID, AccountID: f.account.ID, AgentID: f.agent.ID, MaxActive: 17}).Error)
	status, _, _ = f.application(t, p, reviewed, true)
	require.Equal(t, 409, status)
	status, reviewed, _ = f.application(t, p, input, false)
	require.Equal(t, 200, status)
	require.NoError(t, f.db.Model(&p).Update("revision", p.Revision+1).Error)
	status, _, _ = f.application(t, p, reviewed, true)
	require.Equal(t, 409, status)
	var count int64
	require.NoError(t, f.db.Model(&models.InboxRoutingConfiguration{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxRoutingPolicyIgnoresForgedAdminFlagsAndRemovedMembership(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	person := testutil.CreateTestUser(t, f.db, f.org.ID)
	r := testutil.NewGETRequest(t)
	testutil.SetFullAuthContext(r, f.org.ID, person.ID, nil, true)
	require.NoError(t, f.a.ListInboxRoutingPolicies(r))
	require.Equal(t, 403, r.RequestCtx.Response.StatusCode())
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "routing-manager", []string{"teams:read", "teams:write"})
	manager := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.db.Where("organization_id=? AND user_id=?", f.org.ID, manager.ID).Delete(&models.UserOrganization{}).Error)
	r = testutil.NewGETRequest(t)
	testutil.SetFullAuthContext(r, f.org.ID, manager.ID, &role.ID, false)
	require.NoError(t, f.a.ListInboxRoutingPolicies(r))
	require.Equal(t, 403, r.RequestCtx.Response.StatusCode(), "removed organisation membership cannot inherit a native role")
}
