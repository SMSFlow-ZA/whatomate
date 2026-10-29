package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

// Only the external provider boundary is fake: handlers, webhook processing,
// durable attempts, authorization and dispatch use the real application code.
type fallbackAcceptanceProvider struct {
	channels.Provider
	sent []channels.Message
}

func (*fallbackAcceptanceProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "fallback-acceptance", Name: "Synthetic SMS", Version: "1", APIVersion: 1, Channel: "sms"}
}

func (*fallbackAcceptanceProvider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	return channels.ConnectionInfo{Capabilities: channels.Capabilities{Text: true, DeliveryReports: true}}, nil
}

func (p *fallbackAcceptanceProvider) Send(_ context.Context, _ channels.Connection, message channels.Message) (channels.Submission, error) {
	p.sent = append(p.sent, message)
	return channels.Submission{ClientMessageID: message.ClientMessageID, ProviderMessageID: "synthetic-sms", Status: "submitted"}, nil
}

func newFallbackAcceptanceConnection(t *testing.T, app *App, orgID uuid.UUID) (*models.ChannelConnection, *fallbackAcceptanceProvider) {
	t.Helper()
	p := &fallbackAcceptanceProvider{}
	registry := channels.NewRegistry()
	require.NoError(t, registry.Register(p))
	app.Config = &config.Config{App: config.AppConfig{EncryptionKey: strings.Repeat("k", 32)}}
	app.Channels = &channelapp.Service{DB: app.DB, Providers: registry, EncryptionKey: app.Config.App.EncryptionKey}
	c, err := app.Channels.CreateConnection(context.Background(), orgID, "Synthetic fallback", p.Descriptor().ID, channels.Credentials{"fixture": "synthetic"}, 3)
	require.NoError(t, err)
	t.Cleanup(func() {
		app.WaitForBackgroundTasks()
		require.NoError(t, app.DB.Where("organization_id = ?", orgID).Delete(&models.ChannelAttempt{}).Error)
		require.NoError(t, app.DB.Model(&models.ChannelConnection{}).Where("id = ?", c.ID).Update("enabled", false).Error)
	})
	return c, p
}

func fallbackAcceptanceAttempts(t *testing.T, app *App, id uuid.UUID) map[string]models.ChannelAttempt {
	t.Helper()
	var attempts []models.ChannelAttempt
	require.NoError(t, app.DB.Where("message_id = ?", id).Find(&attempts).Error)
	require.Len(t, attempts, 2, "one logical message must retain exactly one attempt per channel")
	byChannel := map[string]models.ChannelAttempt{}
	for _, attempt := range attempts {
		require.Equal(t, id, attempt.LogicalMessageID)
		byChannel[attempt.Channel] = attempt
	}
	require.Contains(t, byChannel, "whatsapp")
	require.Contains(t, byChannel, "sms")
	return byChannel
}

func TestInboxAPIFallbackWaitsForEligibleWebhookAndExposesBothAttempts(t *testing.T) {
	app, org, account, contact, _ := newGraphTestFixtures(t)
	require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
	c, sms := newFallbackAcceptanceConnection(t, app, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	r := testutil.NewJSONRequest(t, map[string]any{
		"type": "text", "whatsapp_account": account.Name, "content": map[string]string{"body": "Your order is ready"},
		"sms_fallback": map[string]any{"enabled": true, "connection_id": c.ID, "consent_confirmed": true},
	})
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", contact.ID.String())
	require.NoError(t, app.SendMessage(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	app.WaitForBackgroundTasks()
	var response struct{ Data struct{ ID uuid.UUID } }
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(r), &response))
	id := response.Data.ID
	require.NotEqual(t, uuid.Nil, id)
	attempts := fallbackAcceptanceAttempts(t, app, id)
	require.Equal(t, "sent", attempts["whatsapp"].Status)
	require.Equal(t, "waiting", attempts["sms"].Status)
	require.Nil(t, attempts["sms"].DueAt, "the pilot does not opt in to timeout fallback")
	require.NoError(t, app.Channels.Dispatch(context.Background(), time.Now().Add(24*time.Hour)))
	require.Empty(t, sms.sent, "even an old unread message is not eligible without a confirmed failure")
	var message models.Message
	require.NoError(t, app.DB.First(&message, "id = ?", id).Error)
	require.NotEmpty(t, message.WhatsAppMessageID)
	app.updateMessageStatus(message.WhatsAppMessageID, "failed", []WebhookStatusError{{Code: 131026}})
	require.Equal(t, "queued", fallbackAcceptanceAttempts(t, app, id)["sms"].Status)
	for range 2 {
		require.NoError(t, app.Channels.Dispatch(context.Background(), time.Now().Add(time.Second)))
		app.updateMessageStatus(message.WhatsAppMessageID, "failed", []WebhookStatusError{{Code: 131026}})
	}
	require.Len(t, sms.sent, 1, "replayed webhooks and dispatch must not submit twice")
	require.Equal(t, "Your order is ready", sms.sent[0].Text)
	require.Equal(t, "+27820000000", sms.sent[0].Destination)
	require.True(t, sms.sent[0].CheckOptOuts)
	attempts = fallbackAcceptanceAttempts(t, app, id)
	require.Equal(t, "failed", attempts["whatsapp"].Status)
	require.Equal(t, "submitted", attempts["sms"].Status)
	require.Equal(t, attempts["sms"].ID.String(), sms.sent[0].ClientMessageID)

	read := testutil.NewGETRequest(t)
	testutil.SetAuthContext(read, org.ID, user.ID)
	testutil.SetPathParam(read, "id", contact.ID.String())
	require.NoError(t, app.GetMessages(read))
	require.Equal(t, 200, testutil.GetResponseStatusCode(read))
	var history struct {
		Data struct{ Messages []MessageResponse }
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(read), &history))
	require.Len(t, history.Data.Messages, 1, "the inbox must show one bubble with two delivery attempts")
	require.Equal(t, id, history.Data.Messages[0].ID)
	require.Len(t, history.Data.Messages[0].DeliveryAttempts, 2)
	require.NoError(t, app.DB.First(contact, "id = ?", contact.ID).Error)
	require.Nil(t, contact.LastInboundAt, "outbound SMS must not open the WhatsApp session window")
}

func TestInboxAPIDeliveredUnreadCancelsFallbackBeforeDispatch(t *testing.T) {
	app, org, account, contact, _ := newGraphTestFixtures(t)
	contact.PhoneNumber = "27820000000"
	require.NoError(t, app.DB.Model(contact).Update("phone_number", contact.PhoneNumber).Error)
	c, sms := newFallbackAcceptanceConnection(t, app, org.ID)
	message, err := app.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: account, Contact: contact, Type: models.MessageTypeText, Content: "Delivered, still unread",
		Fallback: &SMSFallbackOptions{Enabled: true, ConnectionID: c.ID, ConsentConfirmed: true, TimeoutEnabled: true, TimeoutMinutes: 1},
	}, MessageSendOptions{})
	require.NoError(t, err)
	require.NoError(t, app.DB.First(message, "id = ?", message.ID).Error)
	app.updateMessageStatus(message.WhatsAppMessageID, "delivered", nil)
	app.updateMessageStatus(message.WhatsAppMessageID, "failed", []WebhookStatusError{{Code: 131026}})
	require.NoError(t, app.Channels.Dispatch(context.Background(), time.Now().Add(24*time.Hour)))
	require.Empty(t, sms.sent)
	attempts := fallbackAcceptanceAttempts(t, app, message.ID)
	require.Equal(t, "delivered", attempts["whatsapp"].Status)
	require.Equal(t, "cancelled", attempts["sms"].Status)
	require.Equal(t, "whatsapp_delivered", attempts["sms"].ErrorCode)
	require.NoError(t, app.DB.First(message, "id = ?", message.ID).Error)
	require.Equal(t, models.MessageStatusDelivered, message.Status, "late failure must not regress delivered to failed or read")
}

func TestUnifiedFallbackRejectsAuthenticationUncertaintyAndSuppression(t *testing.T) {
	for _, tc := range []struct{ name, response, wantPrimary, wantSMS string }{
		{"authentication", `{"error":{"code":190,"message":"Synthetic authentication rejection"}}`, "failed", "waiting"},
		{"uncertain_submission", `{"unexpected":"missing submission evidence"}`, "unknown", "waiting"},
		{"suppressed_after_enqueue", `{"messages":[{"id":"wamid.suppressed"}]}`, "failed", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, org, account, contact, _ := newGraphTestFixtures(t)
			contact.PhoneNumber = "27820000000"
			require.NoError(t, app.DB.Model(contact).Update("phone_number", contact.PhoneNumber).Error)
			c, sms := newFallbackAcceptanceConnection(t, app, org.ID)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.name == "authentication" {
					w.WriteHeader(http.StatusUnauthorized)
				}
				_, _ = w.Write([]byte(tc.response))
			}))
			t.Cleanup(server.Close)
			app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, server.URL)
			message, err := app.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
				Account: account, Contact: contact, Type: models.MessageTypeText, Content: "Controlled fallback",
				Fallback: &SMSFallbackOptions{Enabled: true, ConnectionID: c.ID, ConsentConfirmed: true, TimeoutEnabled: true, TimeoutMinutes: 1},
			}, MessageSendOptions{})
			require.NoError(t, err)
			if tc.name == "suppressed_after_enqueue" {
				require.NoError(t, app.DB.Model(contact).Update("sms_opt_out", true).Error)
				app.updateMessageStatus("wamid.suppressed", "failed", []WebhookStatusError{{Code: 131026}})
			}
			require.NoError(t, app.Channels.Dispatch(context.Background(), time.Now().Add(24*time.Hour)))
			require.Empty(t, sms.sent)
			attempts := fallbackAcceptanceAttempts(t, app, message.ID)
			require.Equal(t, tc.wantPrimary, attempts["whatsapp"].Status)
			require.Equal(t, tc.wantSMS, attempts["sms"].Status)
		})
	}
}

func TestInboxAPIFallbackIsOffWithoutExplicitOptIn(t *testing.T) {
	app, org, account, contact, _ := newGraphTestFixtures(t)
	require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
	_, sms := newFallbackAcceptanceConnection(t, app, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	r := testutil.NewJSONRequest(t, map[string]any{"type": "text", "whatsapp_account": account.Name, "content": map[string]string{"body": "WhatsApp only"}})
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", contact.ID.String())
	require.NoError(t, app.SendMessage(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	app.WaitForBackgroundTasks()
	var message models.Message
	require.NoError(t, app.DB.First(&message, "organization_id = ? AND direction = 'outgoing'", org.ID).Error)
	app.updateMessageStatus(message.WhatsAppMessageID, "failed", []WebhookStatusError{{Code: 131026}})
	require.NoError(t, app.Channels.Dispatch(context.Background(), time.Now().Add(24*time.Hour)))
	require.Empty(t, sms.sent)
	var count int64
	require.NoError(t, app.DB.Model(&models.ChannelAttempt{}).Where("message_id = ?", message.ID).Count(&count).Error)
	require.Zero(t, count, "an eligible error does not implicitly enroll the message in fallback")
}

func TestWhatsAppBotFallbackDispatchAndNodeReplaySubmitOnlyOnce(t *testing.T) {
	app, org, account, contact, session := newGraphTestFixtures(t)
	contact.PhoneNumber = "27820000000"
	require.NoError(t, app.DB.Model(contact).Update("phone_number", contact.PhoneNumber).Error)
	c, sms := newFallbackAcceptanceConnection(t, app, org.ID)
	var primarySends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primarySends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.bot-fallback"}]}`))
	}))
	t.Cleanup(server.Close)
	app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, server.URL)
	flowID, inputID := uuid.New(), uuid.New()
	session.CurrentFlowID, session.ChannelInboundMessageID = &flowID, &inputID
	node := ChatNode{ID: "fallback-message", Type: ChatNodeMessage, Config: map[string]any{"delivery_options": map[string]any{
		"channel": "whatsapp", "connection_id": c.ID.String(), "consent_confirmed": true, "fallback_enabled": true, "timeout_enabled": false,
	}}}
	state := &chatNodeCtx{account: account, contact: contact, session: session}
	require.NoError(t, app.sendWhatsAppBotText(&node, state, "Bot order update"))
	var message models.Message
	require.NoError(t, app.DB.First(&message, "organization_id = ? AND direction = 'outgoing'", org.ID).Error)
	require.Equal(t, "waiting", fallbackAcceptanceAttempts(t, app, message.ID)["sms"].Status)
	require.NoError(t, app.Channels.Dispatch(context.Background(), time.Now().Add(24*time.Hour)))
	require.Empty(t, sms.sent)
	app.updateMessageStatus("wamid.bot-fallback", "failed", []WebhookStatusError{{Code: 131026}})
	for range 2 {
		require.NoError(t, app.sendWhatsAppBotText(&node, state, "Bot order update"))
		require.NoError(t, app.Channels.Dispatch(context.Background(), time.Now().Add(time.Second)))
	}
	require.EqualValues(t, 1, primarySends.Load())
	require.Len(t, sms.sent, 1)
	require.Equal(t, "Bot order update", sms.sent[0].Text)
	require.Equal(t, "submitted", fallbackAcceptanceAttempts(t, app, message.ID)["sms"].Status)
	var count int64
	require.NoError(t, app.DB.Model(&models.Message{}).Where("organization_id = ? AND direction = 'outgoing'", org.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, app.DB.First(contact, "id = ?", contact.ID).Error)
	require.Nil(t, contact.LastInboundAt)
}
