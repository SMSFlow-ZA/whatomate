package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxQualityPolicyVersionSurvivesEditsRemovalAndPartialEvents(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	p := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 1, NextReplyMinutes: 2, ResolutionMinutes: 60}
	require.NoError(t, db.Create(&p).Error)
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	a.recordInboxInbound(account, contact, "quality-open", start, nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	require.NotNil(t, c.SLAPolicyID)
	original := *c.SLAPolicyID
	require.NoError(t, db.Model(&p).Update("first_reply_minutes", 30).Error)
	// Send completion callers may supply identifiers only. These must never
	// replace the assigned policy or project from a zero queue-entry timestamp.
	partial := models.InboxCase{BaseModel: models.BaseModel{ID: c.ID}, OrganizationID: org.ID, AccountID: account.ID}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return appendInboxActivity(tx, partial, models.InboxCaseActivity{Kind: "workflow_sent"})
	}))
	require.NoError(t, db.First(&c, "id = ?", c.ID).Error)
	require.Equal(t, original, *c.SLAPolicyID)
	reply := start.Add(time.Minute + 500*time.Millisecond)
	require.NoError(t, db.Model(&c).Update("first_human_reply_at", reply).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return inboxquality.Sync(tx, &c, time.Now().UTC()) }))
	var first models.InboxSLAObligation
	require.NoError(t, db.First(&first, "case_id = ? AND kind = 'first_reply'", c.ID).Error)
	require.Equal(t, "breached", first.Outcome)
	require.False(t, first.Legacy)
	require.NoError(t, db.Delete(&p).Error)
	snapshots, err := a.inboxSLASnapshots(org.ID, []models.InboxCase{c}, reply)
	require.NoError(t, err)
	require.Equal(t, "breached", snapshots[c.ID].FirstReply.Status)
}

func TestInboxQualityLegacyProvenanceIsPerCase(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	p := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 15, NextReplyMinutes: 30, ResolutionMinutes: 60}
	require.NoError(t, db.Create(&p).Error)
	old := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC().Add(-time.Hour), nil, nil)
	require.NoError(t, db.Create(&old).Error)
	require.NoError(t, inboxquality.Backfill(db))
	require.NoError(t, db.First(&old, "id = ?", old.ID).Error)
	require.True(t, old.SLALegacy)
	newContact := testutil.CreateTestContact(t, db, org.ID)
	current := newInboxCase(inboxThread{org.ID, account.ID, newContact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&current).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return inboxquality.Sync(tx, &current, time.Now().UTC()) }))
	var target models.InboxSLAObligation
	require.NoError(t, db.First(&target, "case_id = ?", current.ID).Error)
	require.False(t, target.Legacy)
	require.NoError(t, inboxquality.Backfill(db))
	var count int64
	require.NoError(t, db.Model(&models.InboxSLAObligation{}).Where("case_id = ?", old.ID).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestInboxQualityRefreshesProjectionsAfterPreviousImageWrites(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	p := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 30, NextReplyMinutes: 60, ResolutionMinutes: 480, Timezone: "UTC"}
	require.NoError(t, db.Create(&p).Error)
	a.recordInboxInbound(account, contact, "projection-recovery", time.Now().UTC(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	original := *c.SLAPolicyID
	require.Equal(t, c.Version, c.SLAProjectionVersion)
	// Simulate the previous application, which updates existing case columns and
	// activity but does not know about the new deadline projection columns.
	require.NoError(t, db.Model(&models.InboxCase{}).Where("id=?", c.ID).Updates(map[string]any{"status": models.InboxCaseWaiting, "version": c.Version + 1}).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&models.InboxCaseActivity{OrganizationID: org.ID, AccountID: account.ID, CaseID: c.ID, Kind: "updated", FromStatus: models.InboxCaseOpen, ToStatus: models.InboxCaseWaiting, OccurredAt: &now}).Error)
	require.NoError(t, db.Delete(&p).Error)
	require.NoError(t, inboxquality.Backfill(db))
	require.NoError(t, db.First(&c, "id=?", c.ID).Error)
	require.Equal(t, c.Version, c.SLAProjectionVersion)
	require.Equal(t, original, *c.SLAPolicyID)
	require.False(t, c.SLALegacy)
	var resolution models.InboxSLAObligation
	require.NoError(t, db.First(&resolution, "case_id=? AND kind='resolution'", c.ID).Error)
	require.True(t, resolution.Paused)
}

func TestInboxQualityRecoveryReconstructsEachReplyCycle(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	p := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 30, NextReplyMinutes: 30, ResolutionMinutes: 480, Timezone: "UTC"}
	require.NoError(t, db.Create(&p).Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, start, nil, nil)
	first, pending := start.Add(time.Minute), start.Add(2*time.Minute)
	c.FirstHumanReplyAt, c.LastHumanReplyAt, c.NextReplyFrom = &first, &first, &pending
	require.NoError(t, db.Create(&c).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return inboxquality.Sync(tx, &c, start) }))
	last := start.Add(100 * time.Minute)
	require.NoError(t, db.Model(&c).Updates(map[string]any{"version": c.Version + 4, "last_human_reply_at": last, "next_reply_from": nil}).Error)
	for _, item := range []struct {
		kind    string
		minutes int
	}{{"human_reply", 1}, {"incoming_message", 2}, {"human_reply", 10}, {"incoming_message", 20}, {"human_reply", 100}} {
		at := start.Add(time.Duration(item.minutes) * time.Minute)
		require.NoError(t, db.Create(&models.InboxCaseActivity{OrganizationID: org.ID, AccountID: account.ID, CaseID: c.ID, Kind: item.kind, OccurredAt: &at}).Error)
	}
	require.NoError(t, inboxquality.Backfill(db))
	var cycles []models.InboxSLAObligation
	require.NoError(t, db.Where("case_id=? AND kind='next_reply'", c.ID).Order("started_at").Find(&cycles).Error)
	require.Len(t, cycles, 2)
	require.WithinDuration(t, start.Add(10*time.Minute), *cycles[0].CompletedAt, time.Millisecond)
	require.Equal(t, "met", cycles[0].Outcome)
	require.Equal(t, "breached", cycles[1].Outcome)
	require.True(t, cycles[0].Legacy)
	require.True(t, cycles[1].Legacy)
	require.NoError(t, inboxquality.Backfill(db))
	var count int64
	require.NoError(t, db.Model(&models.InboxSLAObligation{}).Where("case_id=? AND kind='next_reply'", c.ID).Count(&count).Error)
	require.EqualValues(t, 2, count)
}
