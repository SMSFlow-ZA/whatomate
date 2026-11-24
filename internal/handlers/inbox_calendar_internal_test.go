package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxCalendarPolicyEditRetainsAssignedVersionAndProjection(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	policy := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID,
		FirstReplyMinutes: 30, NextReplyMinutes: 30, ResolutionMinutes: 120,
		BusinessHours: true, Timezone: "Africa/Johannesburg"}
	for day := time.Monday; day <= time.Friday; day++ {
		policy.Shifts = append(policy.Shifts, inboxsla.Shift{Day: day, StartMinute: 8 * 60, EndMinute: 17 * 60})
	}
	require.NoError(t, db.Create(&policy).Error)
	start := time.Date(2026, 11, 20, 14, 50, 0, 0, time.UTC)
	open := func() models.InboxCase {
		contact := testutil.CreateTestContact(t, db, org.ID)
		app.recordInboxInbound(account, contact, contact.ID.String(), start, nil)
		current, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
		require.NoError(t, err)
		require.NotNil(t, current.SLAPolicyID)
		return current
	}
	before := open()
	policy.DateExceptions = []inboxsla.DateException{{Date: "2026-11-23"}}
	req := testutil.NewJSONRequest(t, policy)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	testutil.SetPathParam(req, "account_id", account.ID.String())
	require.NoError(t, app.SetInboxSLAPolicy(req))
	require.Equal(t, 200, testutil.GetResponseStatusCode(req))
	after := open()
	require.NotEqual(t, *before.SLAPolicyID, *after.SLAPolicyID)
	// Both the display path and persisted obligation use the immutable calendar.
	for _, item := range []struct {
		current models.InboxCase
		day     int
	}{{before, 23}, {after, 24}} {
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return inboxquality.Sync(tx, &item.current, start) }))
		snapshots, err := app.inboxSLASnapshots(org.ID, []models.InboxCase{item.current}, start)
		require.NoError(t, err)
		want := time.Date(2026, 11, item.day, 6, 20, 0, 0, time.UTC)
		require.Equal(t, want, *snapshots[item.current.ID].FirstReply.Deadline)
		var obligation models.InboxSLAObligation
		require.NoError(t, db.First(&obligation, "case_id=? AND kind='first_reply'", item.current.ID).Error)
		require.True(t, want.Equal(*obligation.Deadline), "persisted deadline must be the same instant")
		require.Equal(t, *item.current.SLAPolicyID, obligation.PolicyID)
	}
	var original models.InboxSLAVersion
	require.NoError(t, db.First(&original, "id=?", before.SLAPolicyID).Error)
	require.Empty(t, original.Policy.DateExceptions)
	// A compatible older writer may edit the active record without updating its
	// version reference; EnsureVersion must detect calendar-only changes too.
	changed, err := json.Marshal([]inboxsla.DateException{{Date: "2026-11-23"}, {Date: "2026-11-24"}})
	require.NoError(t, err)
	require.NoError(t, db.Model(&models.InboxSLAPolicy{}).Where("organization_id=? AND account_id=?", org.ID, account.ID).Update("date_exceptions", string(changed)).Error)
	third := open()
	require.NotEqual(t, *after.SLAPolicyID, *third.SLAPolicyID)
	var retained models.InboxSLAVersion
	require.NoError(t, db.First(&retained, "id=?", after.SLAPolicyID).Error)
	require.Equal(t, []inboxsla.DateException{{Date: "2026-11-23"}}, retained.Policy.DateExceptions)
}

func TestInboxCalendarPolicyRejectsUnsupportedOrInvalidExceptionsAtomically(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	for _, input := range []models.InboxSLAPolicy{
		{FirstReplyMinutes: 30, NextReplyMinutes: 30, ResolutionMinutes: 120, DateExceptions: []inboxsla.DateException{{Date: "2026-11-23"}}},
		{FirstReplyMinutes: 30, NextReplyMinutes: 30, ResolutionMinutes: 120, BusinessHours: true, Timezone: "UTC", Shifts: []inboxsla.Shift{{Day: time.Monday, StartMinute: 60, EndMinute: 120}}, DateExceptions: []inboxsla.DateException{{Date: "2026-02-29"}}},
	} {
		req := testutil.NewJSONRequest(t, input)
		testutil.SetAuthContext(req, org.ID, admin.ID)
		testutil.SetPathParam(req, "account_id", account.ID.String())
		require.NoError(t, app.SetInboxSLAPolicy(req))
		require.Equal(t, 400, testutil.GetResponseStatusCode(req))
	}
	var count int64
	require.NoError(t, db.Model(&models.InboxSLAVersion{}).Where("organization_id=?", org.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&models.InboxSLAPolicy{}).Where("organization_id=?", org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxCalendarEmptyLegacyRepresentationDoesNotReplaceVersion(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	policy := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 30, NextReplyMinutes: 30, ResolutionMinutes: 120}
	require.NoError(t, db.Create(&policy).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return inboxquality.EnsureVersion(tx, &policy, true) }))
	original := *policy.VersionID
	policy.DateExceptions = []inboxsla.DateException{}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return inboxquality.EnsureVersion(tx, &policy, false) }))
	require.Equal(t, original, *policy.VersionID)
}

func TestInboxCalendarLegacyUpdatesPreserveCalendarUntilExplicitRemoval(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	policy := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID,
		FirstReplyMinutes: 30, NextReplyMinutes: 30, ResolutionMinutes: 120,
		BusinessHours: true, Timezone: "UTC",
		Shifts:         []inboxsla.Shift{{Day: time.Monday, StartMinute: 8 * 60, EndMinute: 17 * 60}},
		DateExceptions: []inboxsla.DateException{{Date: "2026-11-23"}}}
	require.NoError(t, db.Create(&policy).Error)
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(encoded, &body))
	delete(body, "date_exceptions")
	body["first_reply_minutes"] = 60
	apply := func(status int) {
		req := testutil.NewJSONRequest(t, body)
		req.RequestCtx.Request.Header.SetMethod("PUT")
		testutil.SetAuthContext(req, org.ID, admin.ID)
		testutil.SetPathParam(req, "account_id", account.ID.String())
		require.NoError(t, app.SetInboxSLAPolicy(req))
		require.Equal(t, status, testutil.GetResponseStatusCode(req))
	}
	load := func() models.InboxSLAPolicy {
		var stored models.InboxSLAPolicy
		require.NoError(t, db.First(&stored, "organization_id=? AND account_id=?", org.ID, account.ID).Error)
		return stored
	}
	apply(200)
	stored := load()
	require.Equal(t, 60, stored.FirstReplyMinutes)
	require.Equal(t, policy.DateExceptions, stored.DateExceptions)
	retained := *stored.VersionID
	body["business_hours"] = false
	apply(400) // Changing clocks requires explicit calendar removal.
	require.Equal(t, retained, *load().VersionID)
	body["date_exceptions"] = map[string]any{"date": "2026-11-23"}
	apply(400)
	require.Equal(t, retained, *load().VersionID)
	body["date_exceptions"] = []any{}
	apply(200)
	stored = load()
	require.False(t, stored.BusinessHours)
	require.Empty(t, stored.DateExceptions)
	require.NotEqual(t, retained, *stored.VersionID)
	var prior models.InboxSLAVersion
	require.NoError(t, db.First(&prior, "id=?", retained).Error)
	require.Equal(t, policy.DateExceptions, prior.Policy.DateExceptions)
}

func TestInboxCalendarSnoozeUsesAssignedDateExceptions(t *testing.T) {
	policy := models.InboxSLAPolicy{BusinessHours: true, Timezone: "Africa/Johannesburg",
		Shifts:         []inboxsla.Shift{{Day: time.Monday, StartMinute: 8 * 60, EndMinute: 17 * 60}, {Day: time.Tuesday, StartMinute: 8 * 60, EndMinute: 17 * 60}},
		DateExceptions: []inboxsla.DateException{{Date: "2026-11-23"}, {Date: "2026-11-24", Intervals: []inboxsla.Interval{{StartMinute: 10 * 60, EndMinute: 12 * 60}}}}}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	options := inboxSnoozeOptions(policy, now)
	require.Len(t, options, 3)
	require.Equal(t, "working_day", options[2].Key)
	require.True(t, time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC).Equal(options[2].Until))
	_, offset := options[2].Until.Zone()
	require.Equal(t, 2*3600, offset)
	require.Equal(t, "Africa/Johannesburg", options[2].Timezone)
}
