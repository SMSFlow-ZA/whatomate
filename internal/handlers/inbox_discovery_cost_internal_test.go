package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxDiscoveryRepresentativePlansAndDeepCursorCost(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	t.Cleanup(func() {
		// Keep this large owned fixture out of later whole-database recovery tests.
		// These rows have no messages, activity, obligations or other dependants.
		require.NoError(t, f.db.Unscoped().Where("organization_id=? AND account_id=?", f.org.ID, f.account.ID).Delete(&models.InboxCase{}).Error)
		require.NoError(t, f.db.Unscoped().Where("organization_id=?", f.org.ID).Delete(&models.Contact{}).Error)
	})
	start := time.Now().UTC().Truncate(time.Second).Add(-30 * 24 * time.Hour)
	// All rows belong exclusively to this synthetic test organisation. No
	// messages, sends, users, workflow events or real provider calls are created.
	require.NoError(t, f.db.Exec(`INSERT INTO contacts (id,organization_id,phone_number,profile_name,created_at,updated_at,is_read,tags,metadata)
SELECT gen_random_uuid(),?,'1555'||lpad(i::text,8,'0'),'Synthetic discovery '||i::text,?::timestamptz+i*interval '1 minute',?::timestamptz,true,'[]'::jsonb,'{}'::jsonb FROM generate_series(1,20000) i`, f.org.ID, start, start).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO inbox_cases (id,organization_id,account_id,contact_id,status,priority,version,opened_at,status_changed_at,last_inbound_at,created_at,updated_at,sla_projection_version,sla_policy_assigned,sla_legacy,routing_reason)
SELECT gen_random_uuid(),organization_id,?,id,'open','normal',1,created_at,created_at,created_at+interval '30 seconds',created_at,updated_at,0,false,false,'' FROM contacts WHERE organization_id=?`, f.account.ID, f.org.ID).Error)
	for _, table := range []string{"inbox_cases", "contacts"} {
		require.NoError(t, f.db.Exec("ANALYZE "+table).Error)
	}
	from, before := start.Add(19000*time.Minute), start.Add(19100*time.Minute)
	plan := func(discovery models.InboxQueueDiscovery) string {
		q, err := f.a.inboxQueueQuery(f.org.ID, f.admin.ID, models.InboxQueueFilter{AccountID: &f.account.ID})
		require.NoError(t, err)
		q = applyInboxDiscoveryDates(q, discovery)
		q, expression := orderInboxDiscovery(q, discovery, f.org.ID)
		statement := q.Session(&gorm.Session{DryRun: true}).Select("inbox_cases.id," + expression + " AS discovery_key").Limit(51).Find(&[]inboxQueueRow{}).Statement
		rows, err := f.db.Raw("EXPLAIN (ANALYZE,BUFFERS,FORMAT TEXT) "+statement.SQL.String(), statement.Vars...).Rows()
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		var out strings.Builder
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			out.WriteString(line + "\n")
		}
		require.NoError(t, rows.Err())
		return out.String()
	}
	activityFrom, activityBefore := from.Add(30*time.Second), before.Add(30*time.Second)
	activity := models.InboxQueueDiscovery{Order: "latest_activity", Timezone: "UTC", ActivityFrom: &activityFrom, ActivityBefore: &activityBefore}
	opened := models.InboxQueueDiscovery{Order: "newest", Timezone: "UTC", OpenedFrom: &from, OpenedBefore: &before}
	t.Log("Before discovery indexes (activity):\n" + plan(activity))
	require.NoError(t, database.CreateInboxDiscoveryIndexes(f.db))
	require.NoError(t, database.CreateInboxDiscoveryIndexes(f.db)) // Safe repeated migration.
	require.True(t, f.db.Migrator().HasIndex(&models.InboxCase{}, "idx_inbox_discovery_activity"))
	require.True(t, f.db.Migrator().HasIndex(&models.InboxCase{}, "idx_inbox_discovery_opened"))
	require.NoError(t, f.db.Exec("ANALYZE inbox_cases").Error)
	for _, d := range []models.InboxQueueDiscovery{activity, opened} {
		t.Log("After discovery indexes (" + d.Order + "):\n" + plan(d))
		began := time.Now()
		first := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
		require.Len(t, first.Cases, 50)
		require.EqualValues(t, 100, first.Counts["all"])
		second := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, first.Next))
		require.Len(t, second.Cases, 50)
		require.False(t, second.More)
		t.Logf("20,000 cases, %s range100 with two50-item pages, scoped counts and target metadata: %s", d.Order, time.Since(began))
		require.Less(t, time.Since(began), 5*time.Second, "Coarse owned local/race budget, not a customer SLA")
	}
	// The change marker must also stay bounded for an unrestricted queue, not
	// only for a selective date range. Both pages recompute the scoped marker.
	broad := models.InboxQueueDiscovery{Order: "latest_activity"}
	began := time.Now()
	first := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &broad, ""))
	require.EqualValues(t, 20000, first.Counts["all"])
	second := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &broad, first.Next))
	require.Len(t, second.Cases, 50)
	require.True(t, second.More)
	require.EqualValues(t, 20000, second.Counts["all"])
	t.Logf("20,000 matching cases, two pages with full scoped revision checks, counts and metadata: %s", time.Since(began))
	require.Less(t, time.Since(began), 5*time.Second, "Coarse owned local/race budget, not a customer SLA")
}
