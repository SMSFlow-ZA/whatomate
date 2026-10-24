package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLateWhatsAppSubmissionCannotRegressConfirmedDelivery(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			app, org, account, contact, _ := newGraphTestFixtures(t)
			message := models.Message{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Hello", Status: models.MessageStatusDelivered}
			require.NoError(t, app.DB.Create(&message).Error)
			var sendErr error
			if failed {
				sendErr = errors.New("uncertain HTTP result")
			}
			app.finalizeMessageSend(&message, OutgoingMessageRequest{Account: account, Contact: contact, Type: models.MessageTypeText}, MessageSendOptions{}, "wamid.late", sendErr)
			var stored models.Message
			require.NoError(t, app.DB.First(&stored, "id = ?", message.ID).Error)
			require.Equal(t, models.MessageStatusDelivered, stored.Status)
		})
	}
}

func TestWhatsAppBotSMSActionsReuseStableInputAndNodeIdentifiers(t *testing.T) {
	for _, mode := range []string{"sms", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			app, org, account, contact, session := newGraphTestFixtures(t)
			require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
			contact.PhoneNumber = "27820000000"
			c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
			require.NoError(t, app.DB.Create(&c).Error)
			app.Channels = &channelapp.Service{DB: app.DB}
			var sends atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.bot"}]}`))
			}))
			defer server.Close()
			app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, server.URL)
			flowID, inputID := uuid.New(), uuid.New()
			session.CurrentFlowID = &flowID
			session.ChannelInboundMessageID = &inputID
			delivery := map[string]any{"channel": "whatsapp", "connection_id": c.ID.String(), "consent_confirmed": true, "fallback_enabled": true, "timeout_enabled": false}
			if mode == "sms" {
				delivery["channel"] = "sms"
				delivery["fallback_enabled"] = false
			}
			node := ChatNode{ID: "hello", Type: ChatNodeMessage, Config: map[string]any{"delivery_options": delivery}}
			state := &chatNodeCtx{account: account, contact: contact, session: session}
			require.NoError(t, app.sendWhatsAppBotText(&node, state, "Hello"))
			require.NoError(t, app.sendWhatsAppBotText(&node, state, "Hello"))
			var count int64
			require.NoError(t, app.DB.Model(&models.Message{}).Where("organization_id = ? AND direction = 'outgoing'", org.ID).Count(&count).Error)
			require.EqualValues(t, 1, count, "a replayed bot action must not create another logical message")
			if mode == "sms" {
				require.Zero(t, sends.Load())
			} else {
				require.EqualValues(t, 1, sends.Load())
			}
		})
	}
}

func TestWhatsAppBotButtonsFallbackIsAuthoredAndReplaySafe(t *testing.T) {
	app, org, account, contact, session := newGraphTestFixtures(t)
	require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
	contact.PhoneNumber = "27820000000"
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
	require.NoError(t, app.DB.Create(&c).Error)
	app.Channels = &channelapp.Service{DB: app.DB}
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.rich"}]}`))
	}))
	defer server.Close()
	app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, server.URL)
	flowID, inputID := uuid.New(), uuid.New()
	session.CurrentFlowID = &flowID
	session.ChannelInboundMessageID = &inputID
	options := map[string]any{"channel": "whatsapp", "connection_id": c.ID.String(), "consent_confirmed": true, "fallback_enabled": true}
	node := ChatNode{ID: "choices", Type: ChatNodeButtons, Config: map[string]any{"body": "Choose", "buttons": []any{map[string]any{"id": "yes", "title": "Yes"}}, "delivery_options": options}}
	state := &chatNodeCtx{account: account, contact: contact, session: session}
	_, err := app.execChatButtons(&node, state)
	require.Error(t, err, "rich fallback requires authored SMS text")
	require.Zero(t, sends.Load())
	options["sms_text"] = "Reply YES to continue"
	_, err = app.execChatButtons(&node, state)
	require.NoError(t, err)
	_, err = app.execChatButtons(&node, state)
	require.NoError(t, err)
	require.EqualValues(t, 1, sends.Load())
	var attempts []models.ChannelAttempt
	require.NoError(t, app.DB.Where("organization_id = ? AND channel = 'sms'", org.ID).Find(&attempts).Error)
	require.Len(t, attempts, 1)
	require.Equal(t, "Reply YES to continue", attempts[0].Text)
	node.Config["body"] = "Changed payload"
	_, err = app.execChatButtons(&node, state)
	require.Error(t, err, "same logical ID with a different payload is a conflict")
	require.EqualValues(t, 1, sends.Load())
}

func TestSMSBotPromptRepliesAreDurableAndSeparateFromWhatsApp(t *testing.T) {
	app := newProcessorTestApp(t)
	app.WhatsApp = nil
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
	contact.PhoneNumber = "27820000000"
	flow := models.ChatbotFlow{OrganizationID: org.ID, Name: "SMS welcome", IsEnabled: true, Graph: models.JSONB{"version": 2, "entry_node": "hello", "nodes": []any{
		map[string]any{"id": "hello", "type": "message", "config": map[string]any{"message": "Welcome"}},
		map[string]any{"id": "ask", "type": "prompt", "config": map[string]any{"body": "Your name?", "store_as": "name"}},
		map[string]any{"id": "done", "type": "end", "config": map[string]any{"message": "Thanks {{name}}"}},
	}, "edges": []any{map[string]any{"from": "hello", "to": "ask", "condition": "default"}, map[string]any{"from": "ask", "to": "done", "condition": "default"}}}}
	require.NoError(t, app.DB.Create(&flow).Error)
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}, BotFlowID: &flow.ID, BotConsentConfirmed: true}
	require.NoError(t, app.DB.Create(&c).Error)
	app.Channels = &channelapp.Service{DB: app.DB, EncryptionKey: "test-test-test-test-test-test-test"}
	incoming := models.Message{OrganizationID: org.ID, ContactID: contact.ID, Channel: "sms", ChannelConnectionID: &c.ID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "Hello", Status: models.MessageStatusDelivered}
	require.NoError(t, app.DB.Create(&incoming).Error)
	job := models.ChannelBotJob{OrganizationID: org.ID, ConnectionID: c.ID, IncomingMessageID: incoming.ID, FlowID: flow.ID, Status: "pending"}
	require.NoError(t, app.DB.Create(&job).Error)
	tx := app.DB.Begin()
	var locked models.ChannelConnection
	require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", c.ID).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err := app.ProcessSMSBotJobs(ctx, time.Now())
	cancel()
	tx.Rollback()
	require.NoError(t, err, "a connection locked by settings must leave the bot job pending")
	require.NoError(t, app.ProcessSMSBotJobs(context.Background(), time.Now()))
	var session models.ChatbotSession
	require.NoError(t, app.DB.First(&session, "organization_id = ? AND channel = ?", org.ID, "sms").Error)
	require.Equal(t, "ask", session.CurrentStep)
	require.Empty(t, session.WhatsAppAccount)
	var count int64
	require.NoError(t, app.DB.Model(&models.Message{}).Where("organization_id = ? AND direction = ?", org.ID, models.DirectionOutgoing).Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.NoError(t, app.ProcessSMSBotJobs(context.Background(), time.Now()))
	require.NoError(t, app.DB.Model(&models.Message{}).Where("organization_id = ? AND direction = ?", org.ID, models.DirectionOutgoing).Count(&count).Error)
	require.EqualValues(t, 2, count, "job replay must not resend")
	incoming.ID = uuid.New()
	incoming.Content = "Eugene"
	require.NoError(t, app.DB.Create(&incoming).Error)
	job.ID = uuid.New()
	job.IncomingMessageID = incoming.ID
	job.Status = "pending"
	require.NoError(t, app.DB.Create(&job).Error)
	require.NoError(t, app.ProcessSMSBotJobs(context.Background(), time.Now()))
	var messages []models.Message
	require.NoError(t, app.DB.Where("organization_id = ? AND direction = ?", org.ID, models.DirectionOutgoing).Order("created_at").Find(&messages).Error)
	require.Len(t, messages, 3)
	require.Equal(t, "Thanks Eugene", messages[2].Content)
	require.NoError(t, app.DB.First(contact, "id = ?", contact.ID).Error)
	require.Nil(t, contact.LastInboundAt)
	waSession, fresh := app.getOrCreateSession(org.ID, contact.ID, "", contact.PhoneNumber, 30)
	require.True(t, fresh, "WhatsApp must not reuse the SMS session")
	require.NotEqual(t, session.ID, waSession.ID)
}

func TestSMSBotFailureRollsBackOutgoingQueuesAndSession(t *testing.T) {
	app := newProcessorTestApp(t)
	app.WhatsApp = nil
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
	flow := models.ChatbotFlow{OrganizationID: org.ID, Name: "Invalid SMS graph", IsEnabled: true, Graph: models.JSONB{"version": 2, "entry_node": "hello", "nodes": []any{
		map[string]any{"id": "hello", "type": "message", "config": map[string]any{"message": "This must roll back"}},
		map[string]any{"id": "bad", "type": "message", "config": map[string]any{"message": strings.Repeat("x", 1000)}},
	}, "edges": []any{map[string]any{"from": "hello", "to": "bad", "condition": "default"}}}}
	require.NoError(t, app.DB.Create(&flow).Error)
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 1, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}, BotFlowID: &flow.ID, BotConsentConfirmed: true}
	require.NoError(t, app.DB.Create(&c).Error)
	app.Channels = &channelapp.Service{DB: app.DB}
	incoming := models.Message{OrganizationID: org.ID, ContactID: contact.ID, Channel: "sms", ChannelConnectionID: &c.ID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "Hello", Status: models.MessageStatusDelivered}
	require.NoError(t, app.DB.Create(&incoming).Error)
	job := models.ChannelBotJob{OrganizationID: org.ID, ConnectionID: c.ID, IncomingMessageID: incoming.ID, FlowID: flow.ID, Status: "pending"}
	require.NoError(t, app.DB.Create(&job).Error)
	app.Channels.StopDispatch = true
	require.NoError(t, app.ProcessSMSBotJobs(context.Background(), time.Now()))
	require.NoError(t, app.DB.First(&job, "id = ?", job.ID).Error)
	require.Equal(t, "pending", job.Status)
	app.Channels.StopDispatch = false
	require.NoError(t, app.ProcessSMSBotJobs(context.Background(), time.Now()))
	var count int64
	require.NoError(t, app.DB.Model(&models.Message{}).Where("organization_id = ? AND direction = 'outgoing'", org.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, app.DB.Model(&models.ChatbotSession{}).Where("organization_id = ?", org.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, app.DB.First(&job, "id = ?", job.ID).Error)
	require.Equal(t, "blocked", job.Status)
}
