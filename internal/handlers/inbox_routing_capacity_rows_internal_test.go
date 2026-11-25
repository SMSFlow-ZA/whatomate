package handlers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type routingQueryCounter struct {
	logger.Interface
	queries atomic.Int64
}

func (c *routingQueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	c.queries.Add(1)
	c.Interface.Trace(ctx, begin, fc, err)
}

func TestInboxRoutingCapacityMetadataBatchesAccountsAndHidesUnpermittedCounts(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.apply(t, models.InboxRoutingInherit, 2)
	other := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	require.NoError(t, f.db.Create(&models.InboxAgentAssignmentRate{OrganizationID: f.org.ID, AccountID: other.ID, AgentID: f.agent.ID, MaxAssignments: 1, WindowSeconds: 3600}).Error)
	for _, account := range []uuid.UUID{f.account.ID, f.account.ID, other.ID} {
		require.NoError(t, f.db.Create(&models.InboxAssignmentReceipt{OrganizationID: f.org.ID, AccountID: account, AgentID: f.agent.ID, CaseID: uuid.New(), AssignedAt: time.Now().UTC().Add(-time.Minute)}).Error)
	}
	// Repeated team/report rows must not turn into per-row database lookups or
	// combine the same agent's counts from different accounts.
	rows := make([]InboxCapacityRow, 200)
	contexts := make([]inboxRoutingCapacityContext, len(rows))
	for i := range rows {
		rows[i] = InboxCapacityRow{AgentID: f.agent.ID, Available: true}
		account := f.account.ID
		if i%2 == 1 {
			account = other.ID
		}
		contexts[i] = inboxRoutingCapacityContext{Row: &rows[i], Account: account, Team: f.team.ID, ShowCounts: i%3 != 0}
	}
	count := &routingQueryCounter{Interface: f.db.Logger}
	db := f.db.Session(&gorm.Session{Logger: count})
	require.NoError(t, decorateInboxRoutingCapacity(db, f.org.ID, contexts))
	require.LessOrEqual(t, count.queries.Load(), int64(5), "query count is bounded by a batch, not by account/team rows")
	for i, row := range rows {
		maximum := 2
		if i%2 == 1 {
			maximum = 1
		}
		require.Equal(t, maximum, row.MaxAssignments)
		require.Equal(t, "rate_limited", row.AssignmentReason)
		if i%3 == 0 {
			require.True(t, row.RateDetailsHidden)
			require.Zero(t, row.Assignments)
			require.Nil(t, row.RateResumesAt)
		} else {
			require.False(t, row.RateDetailsHidden)
			require.EqualValues(t, maximum, row.Assignments)
			require.NotNil(t, row.RateResumesAt)
		}
	}
	var receipts int64
	require.NoError(t, f.db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=?", f.org.ID).Count(&receipts).Error)
	require.EqualValues(t, 3, receipts, "metadata reads never consume an assignment")
}
