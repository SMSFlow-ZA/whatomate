package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxMacroAssignmentRespectsCapacity(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Redis: testutil.SetupTestRedis(t), Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	actor := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	agent := testutil.CreateTestUser(t, db, org.ID)
	limit := models.InboxAgentCapacity{OrganizationID: org.ID, AccountID: account.ID, AgentID: agent.ID, MaxActive: 1}
	require.NoError(t, db.Create(&limit).Error)
	now := time.Now().UTC()
	firstContact := testutil.CreateTestContact(t, db, org.ID)
	first := newInboxCase(inboxThread{org.ID, account.ID, firstContact.ID}, now, nil, nil)
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return assignInboxCase(tx, &first, actor.ID, agent.ID, "")
	}))
	contact := testutil.CreateTestContact(t, db, org.ID)
	current := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, now, nil, nil)
	require.NoError(t, db.Create(&current).Error)
	workflow := models.InboxWorkflow{Actions: []models.InboxAction{{Type: "assign", Value: agent.ID.String()}}}
	err := db.Transaction(func(tx *gorm.DB) error {
		return app.runInboxWorkflowActions(tx, &current, contact, actor.ID, workflow, now)
	})
	require.ErrorIs(t, err, errInboxAgentAtCapacity)
	request := testutil.NewJSONRequest(t, map[string]any{})
	require.NoError(t, app.inboxError(request, err))
	require.Equal(t, 409, testutil.GetResponseStatusCode(request))
	require.NoError(t, db.First(&current, "id = ?", current.ID).Error)
	require.Nil(t, current.AssigneeID)
	// The explicit supervisor path retains its recorded capacity exception.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return assignInboxCase(tx, &current, actor.ID, agent.ID, "Reviewed urgent handover")
	}))
	var activity models.InboxCaseActivity
	require.NoError(t, db.Where("case_id = ? AND kind = ?", current.ID, "capacity_override").First(&activity).Error)
	require.Equal(t, "Reviewed urgent handover", activity.Detail)
}
