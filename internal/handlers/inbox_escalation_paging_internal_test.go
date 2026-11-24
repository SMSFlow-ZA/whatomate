package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxEscalationOwnFeedKeysetMaskingAndReadAcknowledgement(t *testing.T) {
	f := newInboxEscalationFixture(t)
	require.NoError(t, f.db.Model(f.org).Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)
	const phoneName = "+12025550123"
	require.NoError(t, f.db.Model(f.contact).Update("profile_name", phoneName).Error)
	other := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithSuperAdmin())
	observed := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	for i := 0; i < 60; i++ {
		deadline := f.start.Add(time.Duration(i+1) * time.Minute)
		obligation := models.InboxSLAObligation{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, AccountID: f.account.ID, CaseID: f.c.ID, PolicyID: *f.c.SLAPolicyID, Kind: "next_reply", StartedAt: f.start.Add(time.Duration(i) * time.Second), Deadline: &deadline, CompletedAt: &observed, Outcome: "met", BudgetMinutes: 30}
		require.NoError(t, f.db.Create(&obligation).Error)
		event := models.InboxSLAEscalationEvent{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, AccountID: f.account.ID, CaseID: f.c.ID, ObligationID: obligation.ID, PolicyVersionID: *f.c.SLAPolicyID, Kind: obligation.Kind, Threshold: inboxSLABreach, SettingsRevision: 1, DueAt: deadline, ObservedAt: observed, AvailableCount: 2}
		require.NoError(t, f.db.Create(&event).Error)
		for _, recipient := range []uuid.UUID{f.admin.ID, other.ID} {
			require.NoError(t, f.db.Create(&models.InboxSLANotice{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, EventID: event.ID, RecipientID: recipient, Outcome: inboxNoticeAvailable, ObservedAt: observed}).Error)
		}
	}
	// A corrupt receipt cannot borrow a foreign organisation's event.
	foreign := newInboxEscalationFixture(t)
	foreign.arm(t)
	foreign.process(t, "first_reply", inboxSLABreach, foreign.start.Add(time.Hour))
	var foreignEvent models.InboxSLAEscalationEvent
	require.NoError(t, f.db.First(&foreignEvent, "organization_id=?", foreign.org.ID).Error)
	require.NoError(t, f.db.Create(&models.InboxSLANotice{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, EventID: foreignEvent.ID, RecipientID: f.admin.ID, Outcome: inboxNoticeAvailable, ObservedAt: observed}).Error)
	type page struct {
		Notices []inboxSLANoticeItem `json:"notices"`
		More    bool                 `json:"has_more"`
		Cursor  string               `json:"next_cursor"`
	}
	r := f.get(t, f.admin.ID)
	require.NoError(t, f.a.ListInboxSLANotices(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var first page
	testutil.ParseEnvelopeResponse(t, r, &first)
	require.Len(t, first.Notices, 50)
	require.True(t, first.More)
	require.NotEmpty(t, first.Cursor)
	require.NotContains(t, string(r.RequestCtx.Response.Body()), phoneName)
	require.NotContains(t, string(r.RequestCtx.Response.Body()), other.ID.String())
	seen := make(map[uuid.UUID]bool)
	for _, item := range first.Notices {
		seen[item.ID] = true
		require.Equal(t, f.account.ID, item.AccountID)
		require.Equal(t, "completed", item.TargetState)
		require.Equal(t, utils.MaskIfPhoneNumber(phoneName), item.ContactName)
	}
	r = f.get(t, f.admin.ID)
	r.RequestCtx.QueryArgs().Set("cursor", first.Cursor)
	require.NoError(t, f.a.ListInboxSLANotices(r))
	var second page
	testutil.ParseEnvelopeResponse(t, r, &second)
	require.Len(t, second.Notices, 10)
	require.False(t, second.More)
	for _, item := range second.Notices {
		require.False(t, seen[item.ID])
		seen[item.ID] = true
	}
	require.Len(t, seen, 60)
	r = f.get(t, other.ID)
	testutil.SetPathParam(r, "id", first.Notices[0].ID.String())
	require.NoError(t, f.a.ReadInboxSLANotice(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	r = f.get(t, f.admin.ID)
	testutil.SetPathParam(r, "id", first.Notices[0].ID.String())
	require.NoError(t, f.a.ReadInboxSLANotice(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	r = f.get(t, f.admin.ID)
	r.RequestCtx.QueryArgs().Set("unread", "true")
	require.NoError(t, f.a.ListInboxSLANotices(r))
	var unread page
	testutil.ParseEnvelopeResponse(t, r, &unread)
	for _, item := range unread.Notices {
		require.NotEqual(t, first.Notices[0].ID, item.ID)
	}
	for key, value := range map[string]string{"cursor": "invalid!", "offset": "50", "account_id": "not-a-uuid", "unread": "maybe"} {
		r = f.get(t, f.admin.ID)
		r.RequestCtx.QueryArgs().Set(key, value)
		require.NoError(t, f.a.ListInboxSLANotices(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r), key)
	}
	// Configuration history uses the same fixed page bound and safe outcomes.
	r = f.get(t, f.admin.ID)
	require.NoError(t, f.a.ListInboxEscalationHistory(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var history struct {
		Events []models.InboxSLAEscalationEvent `json:"events"`
		More   bool                             `json:"has_more"`
	}
	testutil.ParseEnvelopeResponse(t, r, &history)
	require.Len(t, history.Events, 50)
	require.True(t, history.More)
}

func TestInboxEscalationWorkerDrainsBeyondBatchAndIsolatesOneStorageFailure(t *testing.T) {
	f := newInboxEscalationFixture(t)
	f.arm(t)
	for i := 0; i < 12; i++ {
		contact := testutil.CreateTestContact(t, f.db, f.org.ID)
		f.a.recordInboxInbound(f.account, contact, "synthetic-batch:"+uuid.NewString(), f.start.Add(time.Duration(i+1)*time.Second), nil)
	}
	bad := f.obligation(t, "first_reply")
	constraint := "notice_event_fault_" + fmt.Sprintf("%x", f.org.ID[:8])
	require.NoError(t, f.db.Exec("ALTER TABLE inbox_sla_escalation_events ADD CONSTRAINT "+constraint+" CHECK (obligation_id <> '"+bad.ID.String()+"'::uuid)").Error)
	t.Cleanup(func() { f.db.Exec("ALTER TABLE inbox_sla_escalation_events DROP CONSTRAINT IF EXISTS " + constraint) })
	ctx := context.Background()
	start := time.Now()
	require.Error(t, f.a.runInboxEscalationCycle(ctx, f.start.Add(31*time.Minute)))
	f.counts(t, 9, 9) // The failed oldest item does not stop the other claims.
	require.NoError(t, f.db.Exec("ALTER TABLE inbox_sla_escalation_events DROP CONSTRAINT "+constraint).Error)
	require.NoError(t, f.a.runInboxEscalationCycle(ctx, f.start.Add(31*time.Minute)))
	f.counts(t, 13, 13)
	require.NoError(t, f.a.runInboxEscalationCycle(ctx, f.start.Add(32*time.Minute)))
	f.counts(t, 13, 13)
	t.Logf("13 one-recipient internal crossings, one failed transaction/recovery, three cycles: %s", time.Since(start))
	require.Less(t, time.Since(start), 10*time.Second, "Local race-detector coarse bound, not a production SLA")
}

func TestInboxEscalationCalendarProjectionHonoursHolidayAndDST(t *testing.T) {
	f := newResponsePolicyFixture(t)
	t.Cleanup(func() { f.db.Model(f.org).Update("inbox_workflows_enabled", false) })
	zone, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	start := time.Date(2026, 10, 30, 16, 50, 0, 0, zone)
	policy := responseInput("", 30).Targets
	policy.OrganizationID, policy.AccountID = f.org.ID, f.account.ID
	policy.BusinessHours, policy.Timezone = true, zone.String()
	policy.Shifts = []inboxsla.Shift{{Day: time.Friday, StartMinute: 540, EndMinute: 1020}, {Day: time.Monday, StartMinute: 540, EndMinute: 1020}, {Day: time.Tuesday, StartMinute: 540, EndMinute: 1020}}
	policy.DateExceptions = []inboxsla.DateException{{Date: "2026-11-02", Intervals: []inboxsla.Interval{}}}
	require.NoError(t, f.db.Create(&policy).Error)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	f.a.recordInboxInbound(f.account, contact, "synthetic-calendar-notice", start, nil)
	c, err := (inboxThread{f.org.ID, f.account.ID, contact.ID}).latest(f.db)
	require.NoError(t, err)
	fixture := inboxEscalationFixture{f, contact, c, start}
	fixture.arm(t)
	t.Cleanup(func() {
		f.db.Model(&models.InboxSLAEscalationSettings{}).Where("organization_id=?", f.org.ID).Update("enabled", false)
	})
	obligation := fixture.obligation(t, "first_reply")
	risk := time.Date(2026, 11, 3, 9, 14, 0, 0, zone)
	deadline := time.Date(2026, 11, 3, 9, 20, 0, 0, zone)
	require.True(t, obligation.AtRiskAt.Equal(risk))
	require.True(t, obligation.Deadline.Equal(deadline))
	fixture.process(t, "first_reply", inboxSLAAtRisk, risk.Add(-time.Second))
	fixture.counts(t, 0, 0)
	fixture.process(t, "first_reply", inboxSLAAtRisk, risk.Add(time.Second))
	fixture.process(t, "first_reply", inboxSLABreach, deadline.Add(time.Second))
	fixture.counts(t, 2, 2)
	var event models.InboxSLAEscalationEvent
	require.NoError(t, f.db.First(&event, "organization_id=? AND threshold=?", f.org.ID, inboxSLABreach).Error)
	require.True(t, event.DueAt.Equal(deadline))
	require.Equal(t, *c.SLAPolicyID, event.PolicyVersionID)
}

func TestInboxEscalationMaximumRecipientsRemainBounded(t *testing.T) {
	f := newInboxEscalationFixture(t)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "notice-max-roster", []string{"chat:read", "contacts:read"})
	ids := make([]uuid.UUID, 20)
	for i := range ids {
		ids[i] = uuid.New()
		user := models.User{BaseModel: models.BaseModel{ID: ids[i]}, OrganizationID: f.org.ID, Email: ids[i].String() + "@example.invalid", FullName: "Synthetic internal recipient", RoleID: &role.ID, IsActive: true, PasswordHash: "synthetic-no-signin"}
		require.NoError(t, f.db.Create(&user).Error)
	}
	f.arm(t, ids...)
	start := time.Now()
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(time.Hour))
	f.counts(t, 1, 20)
	t.Logf("one crossing with20 current-role/case checks and durable receipts: %s", time.Since(start))
	require.Less(t, time.Since(start), 10*time.Second, "Local race-detector coarse bound, not a production SLA")
}
