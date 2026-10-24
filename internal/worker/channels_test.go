package worker

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSMSCampaignJobRetryDoesNotDuplicateMessage(t *testing.T) {
	w := testWorker(t)
	org := testutil.CreateTestOrganization(t, w.DB)
	user := testutil.CreateTestUser(t, w.DB, org.ID, testutil.WithSuperAdmin())
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
	require.NoError(t, w.DB.Create(&c).Error)
	campaign := models.BulkMessageCampaign{OrganizationID: org.ID, Channel: "sms", ChannelConnectionID: &c.ID, SMSBody: "Hello", SMSConsentConfirmed: true, Name: "SMS campaign", CreatedBy: user.ID, Status: models.CampaignStatusProcessing}
	require.NoError(t, w.DB.Create(&campaign).Error)
	recipient := models.BulkMessageRecipient{CampaignID: campaign.ID, PhoneNumber: "27820000000", RecipientName: "Test", Status: models.MessageStatusPending}
	require.NoError(t, w.DB.Create(&recipient).Error)
	w.Channels = &channelapp.Service{DB: w.DB, EncryptionKey: "test-test-test-test-test-test-test"}
	job := &queue.RecipientJob{CampaignID: campaign.ID, RecipientID: recipient.ID, OrganizationID: org.ID, PhoneNumber: recipient.PhoneNumber, RecipientName: "Test"}
	require.NoError(t, w.HandleRecipientJob(context.Background(), job))
	require.NoError(t, w.HandleRecipientJob(context.Background(), job))
	var rows []models.Message
	require.NoError(t, w.DB.Where("organization_id = ?", org.ID).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "sms", rows[0].Channel)
	var attempts int64
	require.NoError(t, w.DB.Model(&models.ChannelAttempt{}).Where("message_id = ?", rows[0].ID).Count(&attempts).Error)
	require.EqualValues(t, 1, attempts)
	require.NoError(t, w.DB.First(&recipient, "id = ?", recipient.ID).Error)
	require.NotNil(t, recipient.MessageID)
	require.NotEqual(t, uuid.Nil, *recipient.MessageID)
}

func TestCampaignUncertainPrimaryIsNotFailedOrRepeatedAndDeliveryWins(t *testing.T) {
	w := testWorker(t)
	org := testutil.CreateTestOrganization(t, w.DB)
	user := testutil.CreateTestUser(t, w.DB, org.ID, testutil.WithSuperAdmin())
	contact := testutil.CreateTestContact(t, w.DB, org.ID)
	campaign := models.BulkMessageCampaign{OrganizationID: org.ID, Name: "Fallback", CreatedBy: user.ID, Status: models.CampaignStatusProcessing}
	require.NoError(t, w.DB.Create(&campaign).Error)
	message := models.Message{OrganizationID: org.ID, ContactID: contact.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusPending}
	require.NoError(t, w.DB.Create(&message).Error)
	recipient := models.BulkMessageRecipient{CampaignID: campaign.ID, PhoneNumber: "27820000000", Status: models.MessageStatusPending, MessageID: &message.ID}
	require.NoError(t, w.DB.Create(&recipient).Error)
	w.Channels = &channelapp.Service{DB: w.DB}
	primary := models.ChannelAttempt{OrganizationID: org.ID, MessageID: message.ID, LogicalMessageID: message.ID, Channel: "whatsapp", Status: "pending"}
	require.NoError(t, w.DB.Create(&primary).Error)
	require.NoError(t, w.recordChannelPrimary(context.Background(), &message, &recipient, "", errors.New("uncertain")))
	var attempt models.ChannelAttempt
	require.NoError(t, w.DB.First(&attempt, "message_id = ? AND channel='whatsapp'", message.ID).Error)
	require.Equal(t, "unknown", attempt.Status)
	require.NoError(t, w.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, models.MessageStatusPending, message.Status)
	require.NoError(t, w.DB.Model(&message).Update("status", models.MessageStatusDelivered).Error)
	require.NoError(t, w.recordChannelPrimary(context.Background(), &message, &recipient, "wamid.late", errors.New("late uncertain")))
	require.NoError(t, w.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, models.MessageStatusDelivered, message.Status)
}

func TestSMSCampaignMissingPersonalizationFailsOnceWithoutQueueRetry(t *testing.T) {
	w := testWorker(t)
	org := testutil.CreateTestOrganization(t, w.DB)
	user := testutil.CreateTestUser(t, w.DB, org.ID, testutil.WithSuperAdmin())
	campaign := models.BulkMessageCampaign{OrganizationID: org.ID, Name: "SMS invalid", Channel: "sms", SMSBody: "Hello {{name}}", CreatedBy: user.ID, Status: models.CampaignStatusProcessing}
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
	require.NoError(t, w.DB.Create(&c).Error)
	campaign.ChannelConnectionID = &c.ID
	require.NoError(t, w.DB.Create(&campaign).Error)
	recipient := models.BulkMessageRecipient{CampaignID: campaign.ID, PhoneNumber: "27820000000", Status: models.MessageStatusPending}
	require.NoError(t, w.DB.Create(&recipient).Error)
	w.Channels = &channelapp.Service{DB: w.DB}
	job := &queue.RecipientJob{CampaignID: campaign.ID, RecipientID: recipient.ID, OrganizationID: org.ID}
	require.NoError(t, w.HandleRecipientJob(context.Background(), job))
	require.NoError(t, w.HandleRecipientJob(context.Background(), job))
	require.NoError(t, w.DB.First(&recipient, "id = ?", recipient.ID).Error)
	require.Equal(t, models.MessageStatusFailed, recipient.Status)
	require.NoError(t, w.DB.First(&campaign, "id = ?", campaign.ID).Error)
	require.Equal(t, 1, campaign.FailedCount)
	var count int64
	require.NoError(t, w.DB.Model(&models.ChannelAttempt{}).Where("organization_id = ?", org.ID).Count(&count).Error)
	require.Zero(t, count)
}
