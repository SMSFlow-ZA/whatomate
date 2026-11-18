package handlers

import (
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxCapacityConcurrentAssignment(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.InboxAgentCapacity{}))
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	agent := testutil.CreateTestUser(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	limit := models.InboxAgentCapacity{OrganizationID: org.ID, AccountID: account.ID, AgentID: agent.ID, MaxActive: 1}
	require.NoError(t, db.Create(&limit).Error)

	cases := make([]models.InboxCase, 2)
	for i := range cases {
		contact := testutil.CreateTestContact(t, db, org.ID)
		app.recordInboxInbound(account, contact, contact.ID.String(), time.Now().UTC(), nil)
		c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
		require.NoError(t, err)
		cases[i] = c
	}
	var workers sync.WaitGroup
	outcomes := make(chan error, len(cases))
	for _, c := range cases {
		workers.Go(func() {
			outcomes <- db.Transaction(func(tx *gorm.DB) error {
				if err := reserveInboxAgent(tx, c, agent.ID); err != nil {
					return err
				}
				return tx.Model(&models.InboxCase{}).Where("id = ?", c.ID).Update("assignee_id", agent.ID).Error
			})
		})
	}
	workers.Wait()
	close(outcomes)
	accepted, full := 0, 0
	for err := range outcomes {
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, errInboxAgentAtCapacity)
			full++
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, full)
}
