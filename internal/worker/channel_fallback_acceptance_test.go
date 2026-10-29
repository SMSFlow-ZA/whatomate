package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

type campaignFallbackAcceptanceProvider struct {
	channels.Provider
	sent []channels.Message
}

func (*campaignFallbackAcceptanceProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "campaign-fallback-acceptance", Name: "Synthetic SMS", Version: "1", APIVersion: 1, Channel: "sms"}
}

func (*campaignFallbackAcceptanceProvider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	return channels.ConnectionInfo{Capabilities: channels.Capabilities{Text: true, DeliveryReports: true}}, nil
}

func (p *campaignFallbackAcceptanceProvider) Send(_ context.Context, _ channels.Connection, message channels.Message) (channels.Submission, error) {
	p.sent = append(p.sent, message)
	return channels.Submission{ClientMessageID: message.ClientMessageID, ProviderMessageID: "synthetic-campaign-sms", Status: "submitted"}, nil
}

func TestCampaignFallbackWorkerWaitsForFailureAndReplayKeepsOneLogicalMessage(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		name := "eligible_131026"
		if delivered {
			name = "delivered_unread_cancels"
		}
		t.Run(name, func(t *testing.T) {
			w := testWorker(t)
			org := testutil.CreateTestOrganization(t, w.DB)
			user := testutil.CreateTestUser(t, w.DB, org.ID, testutil.WithSuperAdmin())
			account := testutil.CreateTestWhatsAppAccount(t, w.DB, org.ID)
			template := models.Template{OrganizationID: org.ID, WhatsAppAccount: account.Name, Name: "order_update", Language: "en", Category: "UTILITY", Status: "APPROVED", BodyContent: "Order {{1}} is ready"}
			require.NoError(t, w.DB.Create(&template).Error)
			p := &campaignFallbackAcceptanceProvider{}
			registry := channels.NewRegistry()
			require.NoError(t, registry.Register(p))
			w.Channels = &channelapp.Service{DB: w.DB, Providers: registry, EncryptionKey: strings.Repeat("k", 32)}
			ctx := context.Background()
			connection, err := w.Channels.CreateConnection(ctx, org.ID, "Synthetic campaign", p.Descriptor().ID, channels.Credentials{"fixture": "synthetic"}, 3)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, w.DB.Where("organization_id = ?", org.ID).Delete(&models.ChannelAttempt{}).Error)
				require.NoError(t, w.DB.Model(&models.ChannelConnection{}).Where("id = ?", connection.ID).Update("enabled", false).Error)
			})
			campaign := models.BulkMessageCampaign{
				OrganizationID: org.ID, WhatsAppAccount: account.Name, TemplateID: template.ID, Name: "Synthetic fallback campaign", CreatedBy: user.ID,
				Status: models.CampaignStatusProcessing, TotalRecipients: 1,
				SMSFallback: models.JSONB{"enabled": true, "connection_id": connection.ID.String(), "consent_confirmed": true, "text": "Order {{1}} is ready. Reply HELP for assistance."},
			}
			require.NoError(t, w.DB.Create(&campaign).Error)
			recipient := models.BulkMessageRecipient{CampaignID: campaign.ID, PhoneNumber: "27820000000", RecipientName: "Synthetic", TemplateParams: models.JSONB{"1": "A123"}, Status: models.MessageStatusPending}
			require.NoError(t, w.DB.Create(&recipient).Error)
			var primarySends atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				primarySends.Add(1)
				var payload struct {
					Type     string `json:"type"`
					To       string `json:"to"`
					Template struct {
						Name string `json:"name"`
					} `json:"template"`
				}
				if json.NewDecoder(request.Body).Decode(&payload) != nil || payload.Type != "template" || payload.To != "27820000000" || payload.Template.Name != "order_update" {
					response.WriteHeader(http.StatusBadRequest)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{"messages":[{"id":"wamid.campaign-fallback"}]}`))
			}))
			t.Cleanup(server.Close)
			w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, server.URL)
			job := &queue.RecipientJob{CampaignID: campaign.ID, RecipientID: recipient.ID, OrganizationID: org.ID, PhoneNumber: recipient.PhoneNumber}
			require.NoError(t, w.HandleRecipientJob(ctx, job))
			require.NoError(t, w.HandleRecipientJob(ctx, job), "redelivered queue item must reuse its durable primary submission")
			require.EqualValues(t, 1, primarySends.Load())
			require.NoError(t, w.DB.First(&recipient, "id = ?", recipient.ID).Error)
			require.NotNil(t, recipient.MessageID)
			id := *recipient.MessageID
			var sms models.ChannelAttempt
			require.NoError(t, w.DB.First(&sms, "message_id = ? AND channel = 'sms'", id).Error)
			require.Equal(t, "waiting", sms.Status)
			require.Nil(t, sms.DueAt, "campaign fallback timeout defaults off")
			require.Equal(t, "campaign_fallback", sms.Source)
			require.Equal(t, "Order A123 is ready. Reply HELP for assistance.", sms.Text)
			require.NoError(t, w.Channels.Dispatch(ctx, time.Now().Add(24*time.Hour)))
			require.Empty(t, p.sent)
			require.NoError(t, w.Channels.RecordWhatsApp(ctx, id, "failed", 131026))
			if delivered {
				require.NoError(t, w.Channels.RecordWhatsApp(ctx, id, "delivered", 0))
				require.NoError(t, w.DB.Model(&models.Message{}).Where("id = ?", id).Update("status", models.MessageStatusDelivered).Error)
				require.NoError(t, w.Channels.RecordWhatsApp(ctx, id, "failed", 131026), "a late failure must not reopen delivered fallback")
			}
			for range 2 {
				require.NoError(t, w.Channels.Dispatch(ctx, time.Now().Add(time.Second)))
				require.NoError(t, w.HandleRecipientJob(ctx, job))
			}
			require.EqualValues(t, 1, primarySends.Load())
			require.NoError(t, w.DB.First(&sms, "id = ?", sms.ID).Error)
			if delivered {
				require.Empty(t, p.sent)
				require.Equal(t, "cancelled", sms.Status)
				require.Equal(t, "whatsapp_delivered", sms.ErrorCode)
			} else {
				require.Len(t, p.sent, 1)
				require.Equal(t, "submitted", sms.Status)
				require.Equal(t, sms.ID.String(), p.sent[0].ClientMessageID)
				require.Equal(t, "+27820000000", p.sent[0].Destination)
				require.Equal(t, "Order A123 is ready. Reply HELP for assistance.", p.sent[0].Text)
				require.True(t, p.sent[0].CheckOptOuts)
			}
			var messages []models.Message
			require.NoError(t, w.DB.Preload("DeliveryAttempts").Where("organization_id = ?", org.ID).Find(&messages).Error)
			require.Len(t, messages, 1)
			require.Equal(t, id, messages[0].ID)
			require.Len(t, messages[0].DeliveryAttempts, 2)
			for _, attempt := range messages[0].DeliveryAttempts {
				require.Equal(t, id, attempt.LogicalMessageID)
			}
			var contact models.Contact
			require.NoError(t, w.DB.First(&contact, "id = ?", messages[0].ContactID).Error)
			require.Nil(t, contact.LastInboundAt)
		})
	}
}
