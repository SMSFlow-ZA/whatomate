package handlers

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestInboxReviewRepresentativeQueueAndReportCost(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	contacts, cases, messages := []models.Contact{}, []models.InboxCase{}, []models.Message{}
	now := time.Now().UTC()
	for i := 0; i < 1000; i++ {
		contact := models.Contact{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, PhoneNumber: fmt.Sprintf("1202%07d", i), ProfileName: fmt.Sprintf("Synthetic queue %04d", i), WhatsAppAccount: account.Name}
		contacts = append(contacts, contact)
		c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, now.Add(-time.Duration(i)*time.Minute), &user.ID, nil)
		c.LastInboundAt = &now
		cases = append(cases, c)
		messages = append(messages, models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "Synthetic queue performance fixture"})
	}
	require.NoError(t, db.CreateInBatches(&contacts, 200).Error)
	require.NoError(t, db.CreateInBatches(&cases, 200).Error)
	require.NoError(t, db.CreateInBatches(&messages, 200).Error)
	a := &App{DB: db, Log: testutil.NopLogger()}
	var elapsed time.Duration
	for i := 0; i < 6; i++ {
		r := testutil.NewGETRequest(t)
		testutil.SetAuthContext(r, org.ID, user.ID)
		testutil.SetQueryParam(r, "include_counts", "true")
		start := time.Now()
		require.NoError(t, a.ListInboxQueue(r))
		elapsed += time.Since(start)
		var result struct {
			Cases  []inboxQueueRow
			Counts map[string]int64
			More   bool `json:"has_more"`
		}
		testutil.ParseEnvelopeResponse(t, r, &result)
		require.Len(t, result.Cases, 50)
		require.EqualValues(t, 1000, result.Counts["mine"])
		require.True(t, result.More)
	}
	t.Logf("1000-case synthetic queue: six requests including five scoped counts and customer previews; average=%s", elapsed/6)
	for _, report := range []struct {
		name string
		run  func(*fastglue.Request) error
	}{{"workload", a.GetInboxWorkload}, {"live targets", a.GetInboxSLAReport}, {"history", a.GetInboxSLAHistory}} {
		r := testutil.NewGETRequest(t)
		testutil.SetAuthContext(r, org.ID, user.ID)
		start := time.Now()
		require.NoError(t, report.run(r))
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		t.Logf("1000-case synthetic %s response=%s", report.name, time.Since(start))
	}
}

func TestInboxReviewRemovedMemberCannotRouteOrAppearInCapacity(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	removed := testutil.CreateTestUser(t, db, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(removed).Updates(map[string]any{"is_active": true, "is_available": true}).Error)
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Removed member regression", IsActive: true, AssignmentStrategy: models.AssignmentStrategyRoundRobin}
	require.NoError(t, db.Create(&team).Error)
	require.NoError(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: removed.ID}).Error)
	require.NoError(t, db.Where("organization_id=? AND user_id=?", org.ID, removed.ID).Delete(&models.UserOrganization{}).Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, &team.ID)
	require.NoError(t, db.Create(&c).Error)
	a := &App{DB: db, Log: testutil.NopLogger()}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return a.routeEligibleInboxCase(tx, &c) }))
	require.Nil(t, c.AssigneeID, "a removed member must never receive automatic work")
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	require.NoError(t, a.GetInboxWorkload(req))
	var response struct {
		Agents []struct {
			AgentID uuid.UUID `json:"agent_id"`
		}
	}
	testutil.ParseEnvelopeResponse(t, req, &response)
	for _, row := range response.Agents {
		require.NotEqual(t, removed.ID, row.AgentID)
	}
	// Direct assignment also fails, without expanding access or replacing history.
	require.Error(t, db.Transaction(func(tx *gorm.DB) error { return assignInboxCase(tx, &c, admin.ID, removed.ID, "") }))
}

func TestInboxReviewWorkloadIncludesOwnerWithoutTeamAndScopesCounts(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	owner := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), &owner.ID, nil)
	require.NoError(t, db.Create(&c).Error)
	a := &App{DB: db, Log: testutil.NopLogger()}
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, owner.ID)
	require.NoError(t, a.GetInboxWorkload(req))
	var response struct {
		Agents []struct {
			InboxCapacityRow
			TeamName string `json:"team_name"`
		}
		Unique int64 `json:"unique_agents"`
	}
	testutil.ParseEnvelopeResponse(t, req, &response)
	require.Len(t, response.Agents, 1)
	require.Equal(t, owner.ID, response.Agents[0].AgentID)
	require.EqualValues(t, 1, response.Agents[0].Active)
	require.Empty(t, response.Agents[0].TeamName)
	require.EqualValues(t, 1, response.Unique)
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, owner.ID)
	testutil.SetQueryParam(req, "account_id", uuid.NewString())
	require.NoError(t, a.GetInboxWorkload(req))
	testutil.ParseEnvelopeResponse(t, req, &response)
	require.Empty(t, response.Agents)
	require.Zero(t, response.Unique)
}

func TestInboxReviewSnoozeUsesAssignedCalendarAcrossWeekendAndDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	policy := models.InboxSLAPolicy{Timezone: location.String(), BusinessHours: true, Shifts: []inboxsla.Shift{{Day: time.Monday, StartMinute: 480, EndMinute: 1020}}}
	now := time.Date(2026, 3, 6, 16, 0, 0, 0, location)
	options := inboxSnoozeOptions(policy, now)
	require.Len(t, options, 3)
	require.Equal(t, "working_day", options[2].Key)
	require.Equal(t, time.Date(2026, 3, 9, 8, 0, 0, 0, location), options[2].Until)
	_, offset := options[2].Until.Zone()
	require.Equal(t, -4*3600, offset)
	require.True(t, options[1].Until.After(now))
}
