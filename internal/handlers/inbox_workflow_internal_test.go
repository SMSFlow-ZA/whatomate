package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestValidateInboxWorkflow(t *testing.T) {
	actions := []models.InboxAction{
		{Type: "tag", Value: "refund"},
		{Type: "send_text", Value: "We received your refund enquiry."},
	}
	rule := models.InboxWorkflow{Name: "Refund triage", Kind: "rule", Trigger: "incoming_message", Actions: actions}
	require.True(t, validInboxWorkflow(rule))
	rule.Actions = append(actions, models.InboxAction{Type: "send_text", Value: "Duplicate reply"})
	require.False(t, validInboxWorkflow(rule))
	rule.Actions = []models.InboxAction{{Type: "status", Value: "snoozed"}}
	require.False(t, validInboxWorkflow(rule))
	rule.Actions = actions
	rule.Trigger = "delivery_status"
	require.False(t, validInboxWorkflow(rule))
}

func TestInboxReplyPreviewUsesSelectedAccount(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	other := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	now := time.Now().UTC().Truncate(time.Second)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, now.Add(-30*time.Hour), nil, nil)
	text := models.InboxAction{Type: "send_text", Value: "Thanks for the update."}

	addIncoming := func(accountName string, at time.Time) {
		inbound := models.Message{
			BaseModel:      models.BaseModel{ID: uuid.New(), CreatedAt: at},
			OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: accountName,
			Direction: models.DirectionIncoming, MessageType: models.MessageTypeText,
		}
		require.NoError(t, db.Create(&inbound).Error)
	}
	addIncoming(account.Name, now.Add(-24*time.Hour))
	addIncoming(other.Name, now.Add(-time.Hour))
	_, err := app.previewInboxReply(c, *account, *contact, text, now)
	require.ErrorIs(t, err, errInboxSendIneligible)
	addIncoming(account.Name, now.Add(-time.Hour))
	preview, err := app.previewInboxReply(c, *account, *contact, text, now)
	require.NoError(t, err)
	require.Equal(t, text.Value, preview.Text)

	template := models.Template{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, WhatsAppAccount: account.Name,
		Name: "ticket_received", Language: "en", Status: "APPROVED", Category: "UTILITY", BodyContent: "We received your case.",
	}
	require.NoError(t, db.Create(&template).Error)
	templateAction := models.InboxAction{Type: "send_template", Value: template.ID.String()}
	preview, err = app.previewInboxReply(c, *account, *contact, templateAction, now.Add(48*time.Hour))
	require.NoError(t, err)
	require.Equal(t, template.BodyContent, preview.Text)
	require.NoError(t, db.Model(&template).Update("status", "PAUSED").Error)
	_, err = app.previewInboxReply(c, *account, *contact, templateAction, now)
	require.ErrorIs(t, err, errInboxSendIneligible)
}
