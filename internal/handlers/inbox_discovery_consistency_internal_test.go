package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxDiscoveryChangedWorkRequiresRefreshWithoutLeakingOtherAccounts(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	start := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	entries := make([]models.InboxCase, 55)
	for i := range entries {
		entries[i] = f.entry(t, start.Add(time.Duration(i)*time.Second))
	}
	d := models.InboxQueueDiscovery{Order: "latest_activity"}
	filter := models.InboxQueueFilter{Queue: "unassigned"}
	first := f.page(t, f.request(t, f.admin.ID, filter, &d, ""))
	require.True(t, first.More)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	other := newInboxCase(inboxThread{f.org.ID, otherAccount.ID, entries[0].ContactID}, start, nil, nil)
	require.NoError(t, f.db.Create(&other).Error)
	// Shared contact metadata and another account's work are outside this cursor.
	require.NoError(t, f.db.Model(&other).Updates(map[string]any{"priority": models.InboxPriorityUrgent, "version": gorm.Expr("version+1")}).Error)
	require.NoError(t, f.db.Model(&models.Contact{}).Where("id=?", entries[0].ContactID).Update("last_message_at", start.Add(5*time.Hour)).Error)
	second := f.page(t, f.request(t, f.admin.ID, filter, &d, first.Next))
	require.Len(t, second.Cases, 5)
	require.EqualValues(t, 55, second.Counts["all"])
	for _, change := range []string{"priority", "assignment", "new_case", "removed"} {
		t.Run(change, func(t *testing.T) {
			current := f.page(t, f.request(t, f.admin.ID, filter, &d, ""))
			require.NotEmpty(t, current.Next)
			switch change {
			case "priority":
				require.NoError(t, f.db.Model(&entries[0]).Updates(map[string]any{"priority": models.InboxPriorityHigh, "version": gorm.Expr("version+1")}).Error)
			case "assignment":
				require.NoError(t, f.db.Model(&entries[1]).Updates(map[string]any{"assignee_id": f.admin.ID, "version": gorm.Expr("version+1")}).Error)
			case "new_case":
				f.entry(t, start.Add(time.Minute))
			case "removed":
				require.NoError(t, f.db.Delete(&entries[2]).Error)
			}
			r := f.request(t, f.admin.ID, filter, &d, current.Next)
			require.NoError(t, f.a.ListInboxQueue(r))
			require.Equal(t, 409, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
			require.Contains(t, string(r.RequestCtx.Response.Body()), "inbox_queue_changed")
			require.NotContains(t, string(r.RequestCtx.Response.Body()), `"cases"`)
			// Refreshing explicitly obtains a new valid starting page.
			f.page(t, f.request(t, f.admin.ID, filter, &d, ""))
		})
	}
	// A guessed or absent marker is malformed, not a grant to read the old set.
	data, err := base64.RawURLEncoding.DecodeString(first.Next)
	require.NoError(t, err)
	var cursor inboxDiscoveryCursor
	require.NoError(t, json.Unmarshal(data, &cursor))
	for _, revision := range []string{"", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		cursor.Revision = revision
		data, err = json.Marshal(cursor)
		require.NoError(t, err)
		r := f.request(t, f.admin.ID, filter, &d, base64.RawURLEncoding.EncodeToString(data))
		require.NoError(t, f.a.ListInboxQueue(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	}
}

func TestInboxDiscoveryOneResponseKeepsRowsAndCountsInTheSameReadSnapshot(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	entries := make([]models.InboxCase, 55)
	for i := range entries {
		entries[i] = f.entry(t, start.Add(time.Duration(i)*time.Second))
	}
	d := models.InboxQueueDiscovery{Order: "oldest"}
	filter := models.InboxQueueFilter{Queue: "unassigned"}
	r := f.request(t, f.admin.ID, filter, &d, "")
	rowsReady := make(chan struct{})
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	defer resume()
	callback := "discovery_snapshot_" + uuid.NewString()
	require.NoError(t, f.db.Callback().Row().After("gorm:row").Register(callback, func(tx *gorm.DB) {
		if !strings.Contains(tx.Statement.SQL.String(), "AS discovery_key") {
			return
		}
		once.Do(func() {
			close(rowsReady)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				_ = tx.AddError(errors.New("test did not release the read snapshot"))
			}
		})
	}))
	defer func() { require.NoError(t, f.db.Callback().Row().Remove(callback)) }()
	result := make(chan error, 1)
	go func() { result <- f.a.ListInboxQueue(r) }()
	select {
	case <-rowsReady:
	case <-time.After(5 * time.Second):
		resume()
		t.Fatal("queue rows were not acquired")
	}
	// Another connection commits while rows are acquired but before count/target
	// reads. A read-only repeatable-read transaction must still return old data.
	resolved := time.Now().UTC().Truncate(time.Microsecond)
	err := f.db.Model(&entries[0]).Updates(map[string]any{"status": models.InboxCaseResolved, "resolved_at": resolved, "version": gorm.Expr("version+1")}).Error
	resume()
	require.NoError(t, err)
	select {
	case err = <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("queue read did not finish")
	}
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var old inboxDiscoveryPage
	testutil.ParseEnvelopeResponse(t, r, &old)
	require.Len(t, old.Cases, 50)
	require.Equal(t, entries[0].ID, old.Cases[0].ID)
	require.Equal(t, models.InboxCaseOpen, old.Cases[0].Status)
	require.EqualValues(t, 55, old.Counts["unassigned"])
	fresh := f.page(t, f.request(t, f.admin.ID, filter, &d, ""))
	require.EqualValues(t, 54, fresh.Counts["unassigned"])
	for _, row := range fresh.Cases {
		require.NotEqual(t, entries[0].ID, row.ID)
	}
	stale := f.request(t, f.admin.ID, filter, &d, old.Next)
	require.NoError(t, f.a.ListInboxQueue(stale))
	require.Equal(t, 409, testutil.GetResponseStatusCode(stale))
}
