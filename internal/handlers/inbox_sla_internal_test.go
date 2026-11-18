package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxSLAPausesResolutionAndRetainsNextReplyDeadline(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	timeline := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	policy := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 15, NextReplyMinutes: 30, ResolutionMinutes: 120}
	require.NoError(t, db.Create(&policy).Error)
	thread := inboxThread{org.ID, account.ID, contact.ID}
	a.recordInboxInbound(account, contact, "first", timeline, nil)
	current, err := thread.latest(db)
	require.NoError(t, err)

	replied := timeline.Add(10 * time.Minute)
	require.NoError(t, db.Model(&current).Updates(map[string]any{"first_human_reply_at": replied, "last_human_reply_at": replied}).Error)
	wait := timeline.Add(20 * time.Minute)
	require.NoError(t, applyInboxTransition(db, &current, inboxCaseUpdate{Status: models.InboxCaseWaiting}, nil, wait, "updated"))
	paused, err := a.inboxSLASnapshots(org.ID, []models.InboxCase{current}, wait.Add(10*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "met", paused[current.ID].FirstReply.Status)
	require.Equal(t, "paused", paused[current.ID].Resolution.Status)

	firstUnanswered := timeline.Add(80 * time.Minute)
	a.recordInboxInbound(account, contact, "reopen-from-wait", firstUnanswered, nil)
	a.recordInboxInbound(account, contact, "another", firstUnanswered.Add(10*time.Minute), nil)
	current, err = thread.latest(db)
	require.NoError(t, err)
	require.WithinDuration(t, firstUnanswered, *current.NextReplyFrom, time.Millisecond)
	now := firstUnanswered.Add(15 * time.Minute)
	snapshots, err := a.inboxSLASnapshots(org.ID, []models.InboxCase{current}, now)
	require.NoError(t, err)
	require.WithinDuration(t, firstUnanswered.Add(30*time.Minute), *snapshots[current.ID].NextReply.Deadline, time.Millisecond)
	require.WithinDuration(t, timeline.Add(180*time.Minute), *snapshots[current.ID].Resolution.Deadline, time.Millisecond)
}

func TestInboxSLAExactDeadlineCountsAsMet(t *testing.T) {
	from := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	deadline := from.Add(time.Hour)
	got := measureInboxTarget(inboxsla.ElapsedClock(), from, &deadline, deadline, 60, nil)
	require.Equal(t, "met", got.Status)
}
