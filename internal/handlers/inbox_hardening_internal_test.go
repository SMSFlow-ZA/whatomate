package handlers

import (
	"testing"
	"time"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxHardeningHandoffStartsSLAAtHumanQueue(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	old := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	require.NoError(t, db.Create(&models.Message{
		BaseModel:      models.BaseModel{ID: uuid.New(), CreatedAt: old},
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name,
		WhatsAppMessageID: "synthetic-old-bot-input", Direction: models.DirectionIncoming,
		MessageType: models.MessageTypeText,
	}).Error)
	transfer := &models.AgentTransfer{
		BaseModel:      models.BaseModel{ID: uuid.New(), CreatedAt: time.Now().UTC()},
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name,
		Status: models.TransferStatusActive, TransferredAt: time.Now().UTC(),
	}
	a.recordInboxHandoff(account, contact, transfer)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	require.WithinDuration(t, transfer.TransferredAt, c.OpenedAt, time.Millisecond)
	require.NotNil(t, c.LastInboundAt)
	require.WithinDuration(t, old, *c.LastInboundAt, time.Millisecond)
	a.recordInboxHandoff(account, contact, transfer)
	var count int64
	require.NoError(t, db.Model(&models.InboxCase{}).Where("contact_id = ?", contact.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

