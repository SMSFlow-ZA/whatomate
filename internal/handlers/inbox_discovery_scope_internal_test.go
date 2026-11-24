package handlers

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxDiscoveryScopeMaskingAndSharedTeamCannotBeOverridden(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "Discovery assigned agent", []string{"chat:read"})
	agent := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	other := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithSuperAdmin())
	first := f.entry(t, time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond))
	first.AssigneeID = &agent.ID
	require.NoError(t, f.db.Save(&first).Error)
	require.NoError(t, f.db.Model(f.org).Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)
	require.NoError(t, f.db.Model(&models.Contact{}).Where("id=?", first.ContactID).Updates(map[string]any{"profile_name": "+12025550123", "assigned_user_id": agent.ID}).Error)
	accountB := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	otherAccountCase := newInboxCase(inboxThread{f.org.ID, accountB.ID, first.ContactID}, first.OpenedAt.Add(time.Minute), &other.ID, nil)
	require.NoError(t, f.db.Create(&otherAccountCase).Error)
	private := f.entry(t, first.OpenedAt.Add(2*time.Minute))
	private.AssigneeID = &other.ID
	require.NoError(t, f.db.Save(&private).Error)
	d := models.InboxQueueDiscovery{Order: "latest_activity"}
	p := f.page(t, f.request(t, agent.ID, models.InboxQueueFilter{}, &d, ""))
	require.Len(t, p.Cases, 1)
	require.Equal(t, first.ID, p.Cases[0].ID)
	require.NotEqual(t, "+12025550123", p.Cases[0].ContactName)
	require.EqualValues(t, 1, p.Counts["all"])
	p = f.page(t, f.request(t, agent.ID, models.InboxQueueFilter{AccountID: &accountB.ID}, &d, ""))
	require.Empty(t, p.Cases)
	require.Zero(t, p.Counts["all"])
	foreign := newInboxDiscoveryFixture(t)
	foreign.entry(t, time.Now().UTC())
	p = f.page(t, f.request(t, agent.ID, models.InboxQueueFilter{AccountID: &foreign.account.ID}, &d, ""))
	require.Empty(t, p.Cases)
	require.Zero(t, p.Counts["all"])
	teamA := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Discovery team A", IsActive: true}
	teamB := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Discovery team B", IsActive: true}
	require.NoError(t, f.db.Create(&teamA).Error)
	require.NoError(t, f.db.Create(&teamB).Error)
	require.NoError(t, f.db.Create(&models.TeamMember{TeamID: teamA.ID, UserID: agent.ID}).Error)
	require.NoError(t, f.db.Model(&first).Update("team_id", teamA.ID).Error)
	second := f.entry(t, first.OpenedAt.Add(time.Minute))
	second.TeamID, second.AssigneeID = &teamB.ID, &agent.ID
	second.Priority = models.InboxPriorityHigh
	require.NoError(t, f.db.Save(&second).Error)
	view := models.InboxSavedView{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, OwnerID: f.admin.ID, TeamID: &teamA.ID, Name: "Shared discovery", Filters: models.InboxQueueFilter{TeamID: &teamA.ID}, Discovery: models.InboxQueueDiscovery{Order: "newest"}}
	require.NoError(t, f.db.Create(&view).Error)
	filters := models.InboxQueueFilter{TeamID: &teamB.ID, Match: "any", Predicates: []models.InboxPredicate{{Field: "priority", Operator: "eq", Value: json.RawMessage(`"normal"`)}, {Field: "priority", Operator: "eq", Value: json.RawMessage(`"high"`)}}}
	r := f.request(t, agent.ID, filters, &d, "")
	r.RequestCtx.QueryArgs().Set("view_id", view.ID.String())
	p = f.page(t, r)
	require.Len(t, p.Cases, 1)
	require.Equal(t, first.ID, p.Cases[0].ID)
	require.EqualValues(t, 1, p.Counts["all"])
	require.NoError(t, f.db.Where("team_id=? AND user_id=?", teamA.ID, agent.ID).Delete(&models.TeamMember{}).Error)
	r = f.request(t, agent.ID, filters, &d, "")
	r.RequestCtx.QueryArgs().Set("view_id", view.ID.String())
	require.NoError(t, f.a.ListInboxQueue(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
}

func TestInboxDiscoverySavedViewsPreserveLegacyUpdatesAndExplicitReset(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	d := models.InboxQueueDiscovery{Order: "latest_activity", Timezone: "Africa/Johannesburg", ActivityFrom: &from}
	body := map[string]any{"name": "Example discovery view", "filters": models.InboxQueueFilter{AccountID: &f.account.ID}, "discovery": d}
	r := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	require.NoError(t, f.a.CreateInboxSavedView(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var created struct {
		View models.InboxSavedView `json:"view"`
	}
	testutil.ParseEnvelopeResponse(t, r, &created)
	require.Equal(t, "latest_activity", created.View.Discovery.Order)
	require.True(t, created.View.Discovery.ActivityFrom.Equal(from))
	delete(body, "discovery")
	body["name"] = "Legacy rename"
	r = testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	testutil.SetPathParam(r, "id", created.View.ID.String())
	require.NoError(t, f.a.UpdateInboxSavedView(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var row models.InboxSavedView
	require.NoError(t, f.db.First(&row, "id=?", created.View.ID).Error)
	require.Equal(t, "latest_activity", row.Discovery.Order)
	require.True(t, row.Discovery.ActivityFrom.Equal(from))
	// An older binary's update of known columns leaves the additive column intact.
	require.NoError(t, f.db.Table("inbox_saved_views").Where("id=?", row.ID).Update("name", "Previous-image rename").Error)
	require.NoError(t, f.db.First(&row, "id=?", row.ID).Error)
	require.Equal(t, "latest_activity", row.Discovery.Order)
	list := testutil.NewGETRequest(t)
	testutil.SetAuthContext(list, f.org.ID, f.admin.ID)
	require.NoError(t, f.a.ListInboxSavedViews(list))
	var listed struct {
		Views []struct {
			ID           uuid.UUID                  `json:"id"`
			Discovery    models.InboxQueueDiscovery `json:"discovery"`
			Capabilities map[string]bool            `json:"capabilities"`
		} `json:"views"`
		Version int `json:"discovery_version"`
	}
	testutil.ParseEnvelopeResponse(t, list, &listed)
	require.Equal(t, 1, listed.Version)
	require.Len(t, listed.Views, 1)
	require.True(t, listed.Views[0].Capabilities["edit"])
	require.Equal(t, "latest_activity", listed.Views[0].Discovery.Order)
	body["discovery"] = map[string]any{}
	r = testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	testutil.SetPathParam(r, "id", row.ID.String())
	require.NoError(t, f.a.UpdateInboxSavedView(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NoError(t, f.db.First(&row, "id=?", row.ID).Error)
	require.Equal(t, "priority_waiting", row.Discovery.Order)
	require.Nil(t, row.Discovery.ActivityFrom)
	for _, bad := range []any{map[string]any{"order": "unsupported"}, map[string]any{"timezone": "not-a-zone"}, map[string]any{"unknown": "ignored?"}} {
		body["discovery"] = bad
		r = testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
		testutil.SetPathParam(r, "id", row.ID.String())
		require.NoError(t, f.a.UpdateInboxSavedView(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	}
	// Sorting options are absent from the shared workflow-condition type.
	encoded, err := json.Marshal(models.InboxQueueFilter{})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "discovery")
	require.NotContains(t, string(encoded), "opened_from")
}

func TestInboxDiscoveryNearestDeadlineExcludesStaleForeignPausedAndCompletedWork(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	start := time.Now().UTC().Truncate(time.Second).Add(-8 * time.Hour)
	version := models.InboxSLAVersion{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, AccountID: f.account.ID, Policy: models.InboxSLAPolicy{OrganizationID: f.org.ID, AccountID: f.account.ID, ResolutionMinutes: 120, Timezone: "UTC"}}
	require.NoError(t, f.db.Create(&version).Error)
	var expected []uuid.UUID
	for i := 0; i < 65; i++ {
		c := f.entry(t, start.Add(time.Duration(i)*time.Minute))
		c.SLAPolicyID = &version.ID
		c.SLAPolicyAssigned = true
		c.SLAProjectionVersion = c.Version
		if i == 0 {
			c.Priority = models.InboxPriorityLow
		} else {
			c.Priority = models.InboxPriorityHigh
		}
		require.NoError(t, f.db.Save(&c).Error)
		due := c.OpenedAt.Add(120 * time.Minute)
		require.NoError(t, f.db.Create(&models.InboxSLAObligation{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, AccountID: f.account.ID, CaseID: c.ID, PolicyID: version.ID, Kind: "resolution", StartedAt: c.OpenedAt, Deadline: &due, BudgetMinutes: 120}).Error)
		expected = append(expected, c.ID)
	}
	var inactive []uuid.UUID
	for _, state := range []string{"paused", "completed", "wrong_account", "wrong_org", "wrong_policy", "stale", "resolved"} {
		c := f.entry(t, start.Add(-time.Hour))
		c.SLAPolicyID = &version.ID
		c.SLAPolicyAssigned = true
		c.SLAProjectionVersion = c.Version
		if state == "stale" {
			c.SLAProjectionVersion = 0
		}
		if state == "resolved" {
			at := start
			c.Status = models.InboxCaseResolved
			c.ResolvedAt = &at
		}
		require.NoError(t, f.db.Save(&c).Error)
		due := start.Add(-time.Hour)
		o := models.InboxSLAObligation{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, AccountID: f.account.ID, CaseID: c.ID, PolicyID: version.ID, Kind: "resolution", StartedAt: c.OpenedAt, Deadline: &due, BudgetMinutes: 120}
		switch state {
		case "paused":
			o.Paused = true
		case "completed":
			o.CompletedAt = &start
		case "wrong_account":
			o.AccountID = uuid.New()
		case "wrong_org":
			o.OrganizationID = uuid.New()
		case "wrong_policy":
			o.PolicyID = uuid.New()
		}
		require.NoError(t, f.db.Create(&o).Error)
		inactive = append(inactive, c.ID)
	}
	d := models.InboxQueueDiscovery{Order: "nearest_deadline"}
	first := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
	require.Len(t, first.Cases, 50)
	second := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, first.Next))
	require.Len(t, second.Cases, 22)
	all := append(first.Cases, second.Cases...)
	for i, id := range expected {
		require.Equal(t, id, all[i].ID, "deadline position%d", i)
	}
	sort.Slice(inactive, func(i, j int) bool { return inactive[i].String() < inactive[j].String() })
	for i, row := range all[65:] {
		require.Equal(t, inactive[i], row.ID, "stable no-actionable-deadline bucket")
	}
	legacy := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, nil, ""))
	for _, row := range legacy.Cases {
		require.NotEqual(t, expected[0], row.ID, "low-priority oldest deadline must really be beyond the legacy first page")
	}
}

func TestInboxDiscoveryActivityUsesDeduplicatedCustomerAndHumanReplyPaths(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	c := f.entry(t, start)
	var contact models.Contact
	require.NoError(t, f.db.First(&contact, "id=?", c.ContactID).Error)
	incoming := start.Add(time.Minute)
	f.a.recordInboxInbound(f.account, &contact, "synthetic-discovery-entry", incoming, nil)
	f.a.recordInboxInbound(f.account, &contact, "synthetic-discovery-entry", start.Add(30*time.Minute), nil) // Same event cannot move the clock.
	f.a.recordInboxInbound(f.account, &contact, "synthetic-discovery-old", start.Add(30*time.Second), nil)
	require.NoError(t, f.db.First(&c, "id=?", c.ID).Error)
	require.True(t, c.LastInboundAt.Equal(incoming))
	assertActivity := func(at time.Time) {
		end := at.Add(time.Microsecond)
		d := models.InboxQueueDiscovery{Order: "latest_activity", Timezone: "UTC", ActivityFrom: &at, ActivityBefore: &end}
		p := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
		require.Len(t, p.Cases, 1)
		require.Equal(t, c.ID, p.Cases[0].ID)
	}
	assertActivity(incoming)
	// Fake successful automatic sends have no human actor and do not move a case.
	automatic := &models.Message{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: time.Now().UTC()}, OrganizationID: f.org.ID, ContactID: contact.ID}
	f.a.recordInboxHumanReply(f.account, automatic)
	require.NoError(t, appendInboxActivity(f.db, c, models.InboxCaseActivity{Kind: "note", Detail: "Synthetic note, not conversation activity"}))
	require.NoError(t, f.db.Model(&c).Update("priority", models.InboxPriorityUrgent).Error)
	assertActivity(incoming)
	human := &models.Message{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: start.Add(2 * time.Minute)}, OrganizationID: f.org.ID, ContactID: contact.ID, SentByUserID: &f.admin.ID}
	f.a.recordInboxHumanReply(f.account, human)
	require.NoError(t, f.db.First(&c, "id=?", c.ID).Error)
	require.NotNil(t, c.LastHumanReplyAt)
	assertActivity(*c.LastHumanReplyAt)
	second := f.entry(t, start.Add(4*time.Minute))
	at := start.Add(5 * time.Minute)
	second.LastInboundAt = &at
	require.NoError(t, f.db.Save(&second).Error)
	d := models.InboxQueueDiscovery{Order: "longest_unanswered"}
	p := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
	require.Len(t, p.Cases, 2)
	require.Equal(t, second.ID, p.Cases[0].ID)
	require.Equal(t, c.ID, p.Cases[1].ID)
}
