package handlers

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
)

type inboxDiscoveryFixture struct{ responsePolicyFixture }

func newInboxDiscoveryFixture(t *testing.T) inboxDiscoveryFixture {
	t.Helper()
	f := newResponsePolicyFixture(t)
	t.Cleanup(func() { f.db.Model(f.org).Update("inbox_workflows_enabled", false) })
	return inboxDiscoveryFixture{f}
}

func (f inboxDiscoveryFixture) entry(t *testing.T, at time.Time) models.InboxCase {
	t.Helper()
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	c := newInboxCase(inboxThread{f.org.ID, f.account.ID, contact.ID}, at, nil, nil)
	require.NoError(t, f.db.Create(&c).Error)
	return c
}

type inboxDiscoveryPage struct {
	Cases    []inboxQueueRow            `json:"cases"`
	More     bool                       `json:"has_more"`
	Next     string                     `json:"next_cursor"`
	Revision string                     `json:"queue_revision"`
	Paging   string                     `json:"paging"`
	Version  int                        `json:"discovery_version"`
	Selected models.InboxQueueDiscovery `json:"discovery"`
	Counts   map[string]int64           `json:"counts"`
}

func (f inboxDiscoveryFixture) request(t *testing.T, actor uuid.UUID, filters models.InboxQueueFilter, discovery *models.InboxQueueDiscovery, cursor string) *fastglue.Request {
	t.Helper()
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, f.org.ID, actor)
	if filters.AccountID == nil {
		filters.AccountID = &f.account.ID
	}
	encoded, err := json.Marshal(filters)
	require.NoError(t, err)
	r.RequestCtx.QueryArgs().Set("filters", string(encoded))
	r.RequestCtx.QueryArgs().Set("include_counts", "true")
	if discovery != nil {
		encoded, err = json.Marshal(discovery)
		require.NoError(t, err)
		r.RequestCtx.QueryArgs().Set("discovery", string(encoded))
	}
	if cursor != "" {
		r.RequestCtx.QueryArgs().Set("cursor", cursor)
	}
	return r
}

func (f inboxDiscoveryFixture) page(t *testing.T, r *fastglue.Request) inboxDiscoveryPage {
	t.Helper()
	require.NoError(t, f.a.ListInboxQueue(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var page inboxDiscoveryPage
	testutil.ParseEnvelopeResponse(t, r, &page)
	return page
}

func TestInboxDiscoveryOrdersAndKeysetsBeyondTheFirstPage(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	start := time.Now().UTC().Truncate(time.Second).Add(-8 * time.Hour)
	entries := make([]models.InboxCase, 70)
	priorities := []models.InboxCasePriority{models.InboxPriorityLow, models.InboxPriorityUrgent, models.InboxPriorityNormal, models.InboxPriorityHigh}
	for i := range entries {
		c := f.entry(t, start.Add(time.Duration(i/3)*time.Minute))
		inbound := c.OpenedAt.Add(time.Duration(70-i) * time.Minute)
		c.LastInboundAt, c.Priority = &inbound, priorities[i%4]
		if i%5 == 0 {
			reply := inbound.Add(time.Minute)
			c.FirstHumanReplyAt, c.LastHumanReplyAt = &reply, &reply
		}
		if i%7 == 0 && c.FirstHumanReplyAt != nil {
			unanswered := inbound.Add(2 * time.Minute)
			c.LastInboundAt, c.NextReplyFrom = &unanswered, &unanswered
		}
		require.NoError(t, f.db.Save(&c).Error)
		entries[i] = c
	}
	rank := map[models.InboxCasePriority]int{models.InboxPriorityUrgent: 0, models.InboxPriorityHigh: 1, models.InboxPriorityNormal: 2, models.InboxPriorityLow: 3}
	for _, mode := range []string{"priority_waiting", "latest_activity", "longest_unanswered", "newest", "oldest", "nearest_deadline"} {
		t.Run(mode, func(t *testing.T) {
			d := models.InboxQueueDiscovery{Order: mode}
			first := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
			require.Len(t, first.Cases, 50)
			require.True(t, first.More)
			require.NotEmpty(t, first.Next)
			require.Equal(t, "keyset", first.Paging)
			require.EqualValues(t, 70, first.Counts["all"])
			second := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, first.Next))
			require.Len(t, second.Cases, 20)
			require.False(t, second.More)
			require.Empty(t, second.Next)
			expected := append([]models.InboxCase(nil), entries...)
			activity := func(c models.InboxCase) time.Time {
				at := c.OpenedAt
				if c.LastInboundAt != nil && c.LastInboundAt.After(at) {
					at = *c.LastInboundAt
				}
				if c.LastHumanReplyAt != nil && c.LastHumanReplyAt.After(at) {
					at = *c.LastHumanReplyAt
				}
				return at
			}
			unanswered := func(c models.InboxCase) *time.Time {
				if c.FirstHumanReplyAt == nil {
					return &c.OpenedAt
				}
				return c.NextReplyFrom
			}
			sort.Slice(expected, func(i, j int) bool {
				a, b := expected[i], expected[j]
				if mode == "priority_waiting" && rank[a.Priority] != rank[b.Priority] {
					return rank[a.Priority] < rank[b.Priority]
				}
				if mode == "nearest_deadline" {
					return a.ID.String() < b.ID.String()
				}
				if mode == "longest_unanswered" {
					x, y := unanswered(a), unanswered(b)
					if x == nil || y == nil {
						if x != nil {
							return true
						}
						if y != nil {
							return false
						}
						return a.ID.String() < b.ID.String()
					}
					if !x.Equal(*y) {
						return x.Before(*y)
					}
					return a.ID.String() < b.ID.String()
				}
				x, y := a.OpenedAt, b.OpenedAt
				if mode == "latest_activity" {
					x, y = activity(a), activity(b)
				}
				desc := mode == "newest" || mode == "latest_activity"
				if !x.Equal(y) {
					if desc {
						return x.After(y)
					}
					return x.Before(y)
				}
				if desc {
					return a.ID.String() > b.ID.String()
				}
				return a.ID.String() < b.ID.String()
			})
			all := append(first.Cases, second.Cases...)
			seen := map[uuid.UUID]bool{}
			for i, row := range all {
				require.Equal(t, expected[i].ID, row.ID, "position %d", i)
				require.False(t, seen[row.ID])
				seen[row.ID] = true
			}
		})
	}
	legacy := f.request(t, f.admin.ID, models.InboxQueueFilter{}, nil, "")
	legacy.RequestCtx.QueryArgs().Set("offset", "50")
	page := f.page(t, legacy)
	require.Equal(t, "offset", page.Paging)
	require.Len(t, page.Cases, 20)
	require.Empty(t, page.Next)
}

func TestInboxDiscoveryHalfOpenDateBoundsUseCaseActivityAndConsistentCounts(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	from := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC) // New York's DST end day is25 hours.
	before := from.Add(25 * time.Hour)
	left := f.entry(t, from.Add(-time.Microsecond))
	included := f.entry(t, from)
	inside := f.entry(t, before.Add(-time.Microsecond))
	right := f.entry(t, before)
	for _, c := range []models.InboxCase{left, included, inside, right} {
		at := c.OpenedAt
		require.NoError(t, f.db.Model(&c).Update("last_inbound_at", at).Error)
	}
	d := models.InboxQueueDiscovery{Order: "oldest", Timezone: "America/New_York", OpenedFrom: &from, OpenedBefore: &before}
	p := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
	require.Len(t, p.Cases, 2)
	require.Equal(t, included.ID, p.Cases[0].ID)
	require.Equal(t, inside.ID, p.Cases[1].ID)
	require.EqualValues(t, 2, p.Counts["all"])
	require.EqualValues(t, 2, p.Counts["unassigned"])
	require.EqualValues(t, 2, p.Counts["awaiting_reply"])
	// A contact-wide timestamp or a late receipt/note must not move this case.
	require.NoError(t, f.db.Model(&models.Contact{}).Where("id=?", left.ContactID).Update("last_message_at", from.Add(time.Hour)).Error)
	require.NoError(t, appendInboxActivity(f.db, left, models.InboxCaseActivity{Kind: "note", Detail: "Synthetic internal note"}))
	d.OpenedFrom, d.OpenedBefore = nil, nil
	d.ActivityFrom, d.ActivityBefore = &from, &before
	p = f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
	require.Len(t, p.Cases, 2)
	require.EqualValues(t, 2, p.Counts["all"])
	// Only the case's genuine customer timestamp moves it into the range.
	require.NoError(t, f.db.Model(&left).Update("last_inbound_at", from.Add(time.Hour)).Error)
	p = f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
	require.Len(t, p.Cases, 3)
	require.EqualValues(t, 3, p.Counts["all"])
}

func TestInboxDiscoveryRejectsInvalidPreferencesAndMismatchedCursors(t *testing.T) {
	f := newInboxDiscoveryFixture(t)
	for i := 0; i < 55; i++ {
		f.entry(t, time.Now().UTC().Add(time.Duration(i)*time.Second).Truncate(time.Microsecond))
	}
	d := models.InboxQueueDiscovery{Order: "oldest"}
	first := f.page(t, f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, ""))
	for _, raw := range []string{`null`, `[]`, `{"order":"sql;drop table inbox_cases"}`, `{"order":"oldest","arbitrary":true}`, `{"opened_from":"2026-11-24T10:00:00Z"}`, `{"timezone":"Local"}`, `{"timezone":"../etc/passwd"}`, `{"timezone":"UTC","opened_from":"2026-11-24T10:00:00.000000001Z"}`, `{"timezone":"UTC","opened_from":"2026-11-25T00:00:00Z","opened_before":"2026-11-24T00:00:00Z"}`, `{"timezone":"UTC","activity_from":"0001-01-01T00:00:00Z"}`, `{"timezone":"UTC","activity_from":"9999-12-31T23:00:00-02:00"}`, `{"timezone":"UTC","activity_from":"0001-01-01T00:00:00+01:00"}`} {
		r := f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, "")
		r.RequestCtx.QueryArgs().Set("discovery", raw)
		require.NoError(t, f.a.ListInboxQueue(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r), raw)
	}
	for _, change := range []string{"sort", "filter", "actor", "bad_cursor", "offset", "long_cursor"} {
		r := f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, first.Next)
		switch change {
		case "sort":
			r.RequestCtx.QueryArgs().Set("discovery", `{"order":"newest"}`)
		case "filter":
			encoded, _ := json.Marshal(models.InboxQueueFilter{AccountID: &f.account.ID, Priority: models.InboxPriorityHigh})
			r.RequestCtx.QueryArgs().Set("filters", string(encoded))
		case "actor":
			other := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithSuperAdmin())
			testutil.SetAuthContext(r, f.org.ID, other.ID)
		case "bad_cursor":
			r.RequestCtx.QueryArgs().Set("cursor", "not-base64!")
		case "offset":
			r.RequestCtx.QueryArgs().Set("offset", "50")
		case "long_cursor":
			r.RequestCtx.QueryArgs().Set("cursor", string(make([]byte, 1025)))
		}
		require.NoError(t, f.a.ListInboxQueue(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r), change)
	}
	// Cursor contents only bound pagination; they never provide access to a row.
	data, err := base64.RawURLEncoding.DecodeString(first.Next)
	require.NoError(t, err)
	var cursor inboxDiscoveryCursor
	require.NoError(t, json.Unmarshal(data, &cursor))
	cursor.ID = uuid.Nil
	data, err = json.Marshal(cursor)
	require.NoError(t, err)
	r := f.request(t, f.admin.ID, models.InboxQueueFilter{}, &d, base64.RawURLEncoding.EncodeToString(data))
	require.NoError(t, f.a.ListInboxQueue(r))
	require.Equal(t, 400, testutil.GetResponseStatusCode(r))
}
