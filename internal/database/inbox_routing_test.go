package database_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestInboxRoutingReceiptsDescribeCommittedOwnershipIncludingOldWriters(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	schema := "routing_receipts_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, tx.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, tx.Exec("SET LOCAL search_path TO "+schema).Error)
	require.NoError(t, tx.AutoMigrate(&models.InboxCase{}, &models.InboxAssignmentReceipt{}, &models.InboxRoutingPolicy{}, &models.InboxRoutingConfiguration{}, &models.InboxAgentAssignmentRate{}))
	org, account, agent, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	before := time.Now().UTC()
	c := models.InboxCase{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, ContactID: uuid.New(), AssigneeID: &agent, Version: 1, Status: models.InboxCaseOpen, Priority: models.InboxPriorityHigh, OpenedAt: before.Add(-24 * time.Hour), StatusChangedAt: before}
	require.NoError(t, tx.Create(&c).Error)
	require.NoError(t, database.InstallInboxAssignmentReceipts(tx))
	require.NoError(t, database.InstallInboxAssignmentReceipts(tx))
	count := func() int64 {
		var result int64
		require.NoError(t, tx.Model(&models.InboxAssignmentReceipt{}).Count(&result).Error)
		return result
	}
	require.Zero(t, count(), "no assignment history is reconstructed at migration")
	update := func(value any) error {
		// Previous binaries need no new Go API or incremented case version.
		return tx.Exec("UPDATE inbox_cases SET assignee_id=? WHERE id=?", value, c.ID).Error
	}
	require.NoError(t, update(agent))
	require.Zero(t, count(), "re-saving the same owner is not another assignment")
	require.NoError(t, update(nil))
	require.Zero(t, count(), "release does not consume an assignment")
	require.NoError(t, update(agent))
	require.EqualValues(t, 1, count())
	require.NoError(t, update(other))
	require.EqualValues(t, 2, count())
	require.NoError(t, tx.Exec("UPDATE inbox_cases SET status='pending', version=version+1 WHERE id=?", c.ID).Error)
	require.EqualValues(t, 2, count(), "state/version changes do not inflate assignment rates")
	abort := errors.New("synthetic ownership rollback")
	require.ErrorIs(t, tx.Transaction(func(inner *gorm.DB) error {
		require.NoError(t, inner.Exec("UPDATE inbox_cases SET assignee_id=? WHERE id=?", agent, c.ID).Error)
		return abort
	}), abort)
	require.EqualValues(t, 2, count(), "rolled-back claims have no receipt")
	var receipts []models.InboxAssignmentReceipt
	require.NoError(t, tx.Order("id").Find(&receipts).Error)
	for _, receipt := range receipts {
		require.Equal(t, org, receipt.OrganizationID)
		require.Equal(t, account, receipt.AccountID)
		require.Equal(t, c.ID, receipt.CaseID)
		require.False(t, receipt.AssignedAt.Before(before), "use assignment time, not the age of the case")
	}
	require.Equal(t, agent, receipts[0].AgentID)
	require.Equal(t, other, receipts[1].AgentID)
	newCase := c
	newCase.ID, newCase.ContactID = uuid.New(), uuid.New()
	require.NoError(t, tx.Create(&newCase).Error)
	require.EqualValues(t, 3, count(), "a newly created assigned case is one assignment")
	require.NoError(t, database.InstallInboxAssignmentReceipts(tx))
	require.EqualValues(t, 3, count(), "reinstallation never replays completed ownership changes")
	require.NoError(t, tx.Delete(&newCase).Error)
	require.EqualValues(t, 3, count(), "case retirement does not reset the rate ledger")
	for _, rate := range []models.InboxAgentAssignmentRate{
		{MaxAssignments: -1, WindowSeconds: 0},
		{MaxAssignments: 0, WindowSeconds: 60},
		{MaxAssignments: 1, WindowSeconds: 59},
		{MaxAssignments: 10001, WindowSeconds: 3600},
	} {
		rate.OrganizationID, rate.AccountID, rate.AgentID = org, account, uuid.New()
		require.Error(t, tx.Transaction(func(inner *gorm.DB) error { return inner.Create(&rate).Error }), "raw writes cannot install invalid rolling limits")
	}
}
