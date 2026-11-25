package handlers

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func routingRateCase(t *testing.T, db *gorm.DB, org, account uuid.UUID) models.InboxCase {
	t.Helper()
	now := time.Now().UTC()
	c := models.InboxCase{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, ContactID: testutil.CreateTestContact(t, db, org).ID, Status: models.InboxCaseOpen, Priority: models.InboxPriorityNormal, Version: 1, OpenedAt: now, StatusChangedAt: now}
	require.NoError(t, db.Create(&c).Error)
	return c
}

func TestInboxRoutingRateClaimsStayAtomicAcrossWorkersAndRestart(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, database.InstallInboxAssignmentReceipts(db))
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	agent := testutil.CreateTestUser(t, db, org.ID)
	rate := models.InboxAgentAssignmentRate{OrganizationID: org.ID, AccountID: account.ID, AgentID: agent.ID, MaxAssignments: 1, WindowSeconds: 3600}
	require.NoError(t, db.Create(&rate).Error)
	cases := make([]models.InboxCase, 6)
	for i := range cases {
		cases[i] = routingRateCase(t, db, org.ID, account.ID)
	}
	var workers sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, len(cases))
	for _, c := range cases {
		workers.Go(func() {
			<-start
			results <- db.Transaction(func(tx *gorm.DB) error {
				if err := reserveInboxRoutingAssignment(tx, c, agent.ID); err != nil {
					return err
				}
				return tx.Model(&models.InboxCase{}).Where("id=?", c.ID).Update("assignee_id", agent.ID).Error
			})
		})
	}
	close(start)
	workers.Wait()
	close(results)
	assigned, held := 0, 0
	for err := range results {
		if err == nil {
			assigned++
		} else {
			require.ErrorIs(t, err, errInboxAssignmentRate)
			held++
		}
	}
	require.Equal(t, 1, assigned)
	require.Equal(t, 5, held)
	var count int64
	require.NoError(t, db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=? AND account_id=?", org.ID, account.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	var current, waiting models.InboxCase
	require.NoError(t, db.First(&current, "organization_id=? AND account_id=? AND assignee_id=?", org.ID, account.ID, agent.ID).Error)
	require.NoError(t, db.First(&waiting, "organization_id=? AND account_id=? AND assignee_id IS NULL", org.ID, account.ID).Error)
	// A new DB session/process has no in-memory fairness authority to reset.
	restarted := db.Session(&gorm.Session{NewDB: true})
	require.ErrorIs(t, restarted.Transaction(func(tx *gorm.DB) error { return reserveInboxRoutingAssignment(tx, waiting, agent.ID) }), errInboxAssignmentRate)
	require.NoError(t, restarted.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, reserveInboxRoutingAssignment(tx, current, agent.ID))
		return tx.Model(&current).Update("assignee_id", agent.ID).Error
	}))
	require.NoError(t, db.Model(&models.InboxAssignmentReceipt{}).Where("organization_id=? AND account_id=?", org.ID, account.ID).Count(&count).Error)
	require.EqualValues(t, 1, count, "post-restart unchanged-owner retry does not record or consume another slot")
	// Disabling and reapplying a rate must retain the previous assignment truth.
	require.NoError(t, db.Model(&rate).Updates(map[string]any{"max_assignments": 0, "window_seconds": 0}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := reserveInboxRoutingAssignment(tx, waiting, agent.ID); err != nil {
			return err
		}
		return tx.Model(&waiting).Update("assignee_id", agent.ID).Error
	}))
	require.NoError(t, db.Model(&rate).Updates(map[string]any{"max_assignments": 1, "window_seconds": 3600}).Error)
	remaining := routingRateCase(t, db, org.ID, account.ID)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return reserveInboxRoutingAssignment(tx, remaining, agent.ID) }), errInboxAssignmentRate)
	fresh := remaining
	fresh.ID, fresh.AssigneeID = uuid.New(), &agent.ID
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return reserveInboxRoutingAssignment(tx, fresh, agent.ID) }), errInboxAssignmentRate, "an assignee prepared for a new case is not an unchanged stored owner")
	usage, err := inboxAssignmentRatesAt(db, org.ID, account.ID, []uuid.UUID{agent.ID}, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 2, usage[agent.ID].Assignments)
}

func TestInboxRoutingRollingBoundaryAndRecoveryAfterOverrides(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org, account, agent := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	rate := models.InboxAgentAssignmentRate{OrganizationID: org, AccountID: account, AgentID: agent, MaxAssignments: 2, WindowSeconds: 60}
	require.NoError(t, db.Create(&rate).Error)
	for _, seconds := range []int{-60, -59, -30, -10} {
		require.NoError(t, db.Create(&models.InboxAssignmentReceipt{OrganizationID: org, AccountID: account, AgentID: agent, CaseID: uuid.New(), AssignedAt: now.Add(time.Duration(seconds) * time.Second)}).Error)
	}
	// Neither a foreign account nor a foreign organisation can consume this slot.
	for _, scope := range [][2]uuid.UUID{{uuid.New(), account}, {org, uuid.New()}} {
		require.NoError(t, db.Create(&models.InboxAssignmentReceipt{OrganizationID: scope[0], AccountID: scope[1], AgentID: agent, CaseID: uuid.New(), AssignedAt: now}).Error)
	}
	read := func(at time.Time) inboxAssignmentRateUsage {
		rows, err := inboxAssignmentRatesAt(db, org, account, []uuid.UUID{agent}, at)
		require.NoError(t, err)
		return rows[agent]
	}
	usage := read(now)
	require.EqualValues(t, 3, usage.Assignments, "the exact lower boundary has expired")
	require.NotNil(t, usage.ResumesAt)
	require.True(t, now.Add(30*time.Second).Equal(*usage.ResumesAt), "with overrides above the limit, the oldest receipt alone is insufficient")
	usage = read(now.Add(30 * time.Second))
	require.EqualValues(t, 1, usage.Assignments)
	require.Nil(t, usage.ResumesAt)
	// If the database clock moves backwards, future receipts are retained
	// conservatively rather than creating spurious free assignment slots.
	usage = read(now.Add(-15 * time.Second))
	require.EqualValues(t, 4, usage.Assignments)
	usage = read(now.Add(50 * time.Second))
	require.Zero(t, usage.Assignments)
	require.Nil(t, usage.ResumesAt)
}
