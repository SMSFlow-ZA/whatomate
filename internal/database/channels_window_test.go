package database_test

import (
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRestartBackfillNeverOpensWhatsAppWindowFromSMS(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)
	for _, channel := range []string{"sms", "whatsapp", ""} {
		contact := testutil.CreateTestContact(t, db, org.ID)
		message := models.Message{OrganizationID: org.ID, ContactID: contact.ID, Channel: channel, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "hello", Status: models.MessageStatusDelivered}
		require.NoError(t, db.Create(&message).Error)
		if channel == "" {
			require.NoError(t, db.Model(&message).Update("channel", "").Error)
		}
		require.NoError(t, database.BackfillLastInboundAt(db))
		require.NoError(t, db.First(contact, "id = ?", contact.ID).Error)
		if channel == "sms" {
			require.Nil(t, contact.LastInboundAt, "SMS must remain separate after startup/restore")
		} else {
			require.NotNil(t, contact.LastInboundAt, "legacy WhatsApp backfill remains supported")
		}
	}
}
