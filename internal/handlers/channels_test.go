package handlers_test

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

type rotationProvider struct {
	channels.Provider
	validations int
}

func (*rotationProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "rotation", Name: "Rotation fixture", Version: "1", APIVersion: 1, Channel: "sms"}
}
func (p *rotationProvider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	p.validations++
	return channels.ConnectionInfo{Capabilities: channels.Capabilities{Text: true, DeliveryReports: true}}, nil
}
func TestSMPPCredentialRotationCannotChangeExistingClientIdentity(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	p := &rotationProvider{}
	registry := channels.NewRegistry()
	require.NoError(t, registry.Register(p))
	app.Channels = &channelapp.Service{DB: app.DB, Providers: registry, EncryptionKey: strings.Repeat("k", 32)}
	c, err := app.Channels.CreateConnection(context.Background(), org.ID, "Original", "rotation", channels.Credentials{"client_id": "owner-a", "client_secret": "old-secret"}, 3)
	require.NoError(t, err)
	for _, identity := range []string{"owner-b", "owner-a"} {
		r := testutil.NewJSONRequest(t, map[string]any{"name": "Original", "enabled": true, "max_segments": 3, "credentials": map[string]string{"client_id": identity, "client_secret": "new-secret"}})
		testutil.SetAuthContext(r, org.ID, user.ID)
		testutil.SetPathParam(r, "id", c.ID.String())
		require.NoError(t, app.UpdateChannelConnection(r))
		if identity == "owner-b" {
			require.Equal(t, 422, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
			require.Equal(t, 1, p.validations, "different account must be rejected before remote validation")
		} else {
			require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
			require.Equal(t, 2, p.validations)
		}
		require.NotContains(t, string(testutil.GetResponseBody(r)), "new-secret")
	}
}

func TestSMSHTTPClientIdentifierReplaysOneLogicalMessage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMPP", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
	require.NoError(t, app.DB.Create(&c).Error)
	app.Channels = &channelapp.Service{DB: app.DB, EncryptionKey: app.Config.App.EncryptionKey}
	logical := uuid.New()
	for _, text := range []string{"HTTP retry", "HTTP retry", "Different content"} {
		r := testutil.NewJSONRequest(t, map[string]any{"type": "text", "channel": "sms", "channel_connection_id": c.ID, "client_message_id": logical.String(), "consent_confirmed": true, "content": map[string]string{"body": text}})
		testutil.SetAuthContext(r, org.ID, user.ID)
		testutil.SetPathParam(r, "id", contact.ID.String())
		require.NoError(t, app.SendMessage(r))
		if text == "Different content" {
			require.Equal(t, 422, testutil.GetResponseStatusCode(r))
		} else {
			require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
			var response struct{ Data struct{ ID uuid.UUID } }
			require.NoError(t, json.Unmarshal(testutil.GetResponseBody(r), &response))
			require.Equal(t, logical, response.Data.ID)
		}
	}
	var count int64
	require.NoError(t, app.DB.Model(&models.ChannelAttempt{}).Where("message_id = ?", logical).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestChannelNotificationsRequireAuthorizedHTTPReads(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "Limited", []string{"chat:read", "chat:write"})
	a := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	b := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", a.ID).Error)
	msg := models.Message{OrganizationID: org.ID, ContactID: contact.ID, Channel: "sms", Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "confidential SMS", Status: models.MessageStatusDelivered}
	require.NoError(t, app.DB.Create(&msg).Error)
	attempt := models.ChannelAttempt{OrganizationID: org.ID, MessageID: msg.ID, LogicalMessageID: msg.ID, Channel: "sms", Text: "confidential fallback", Status: "delivered"}
	require.NoError(t, app.DB.Create(&attempt).Error)
	hub := websocket.NewHub(app.Log)
	go hub.Run()
	app.WSHub = hub
	ca, cb, foreign := websocket.NewClient(hub, nil, a.ID, org.ID), websocket.NewClient(hub, nil, b.ID, org.ID), websocket.NewClient(hub, nil, b.ID, other.ID)
	for _, c := range []*websocket.Client{ca, cb, foreign} {
		hub.Register(c)
		defer hub.Unregister(c)
	}
	require.NoError(t, app.InitializeChannels())
	for pass := 0; pass < 2; pass++ {
		app.Channels.Notify(org.ID, msg.ID)
		for _, c := range []*websocket.Client{ca, cb} {
			select {
			case frame := <-c.SendChan():
				require.NotContains(t, string(frame), "confidential")
				var event map[string]any
				require.NoError(t, json.Unmarshal(frame, &event))
				require.Equal(t, true, event["payload"].(map[string]any)["channel_invalidation"])
			case <-time.After(time.Second):
				t.Fatal("missing channel invalidation")
			}
		}
		select {
		case <-foreign.SendChan():
			t.Fatal("cross-organization notification")
		default:
		}
		r := testutil.NewGETRequest(t)
		testutil.SetAuthContext(r, org.ID, b.ID)
		testutil.SetPathParam(r, "id", contact.ID.String())
		require.NoError(t, app.GetMessages(r))
		require.Equal(t, 404, testutil.GetResponseStatusCode(r))
		require.NotContains(t, string(testutil.GetResponseBody(r)), "confidential")
		assigned := testutil.NewGETRequest(t)
		testutil.SetAuthContext(assigned, org.ID, a.ID)
		testutil.SetPathParam(assigned, "id", contact.ID.String())
		require.NoError(t, app.GetMessages(assigned))
		if pass == 0 {
			require.Equal(t, 200, testutil.GetResponseStatusCode(assigned))
			require.Contains(t, string(testutil.GetResponseBody(assigned)), "confidential SMS")
		} else {
			require.Equal(t, 404, testutil.GetResponseStatusCode(assigned))
			require.NotContains(t, string(testutil.GetResponseBody(assigned)), "confidential")
		}
		require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", nil).Error)
	}
}

func TestCurrentConversationPolicyDoesNotExposeHistoricalSMS(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "Limited history", []string{"chat:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", user.ID).Error)
	settings := models.ChatbotSettings{OrganizationID: org.ID, AgentAssignment: models.AgentAssignmentConfig{CurrentConversationOnly: true}}
	require.NoError(t, app.DB.Create(&settings).Error)
	cutoff := time.Now().Add(-time.Hour)
	session := models.ChatbotSession{OrganizationID: org.ID, ContactID: contact.ID, Channel: "whatsapp", Status: models.SessionStatusActive, StartedAt: cutoff}
	require.NoError(t, app.DB.Create(&session).Error)
	for i, text := range []string{"old private SMS", "current SMS"} {
		msg := models.Message{OrganizationID: org.ID, ContactID: contact.ID, Channel: "sms", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: text, Status: models.MessageStatusSent}
		msg.CreatedAt = cutoff.Add(time.Duration(i*2-1) * time.Minute)
		require.NoError(t, app.DB.Create(&msg).Error)
	}
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", contact.ID.String())
	require.NoError(t, app.GetMessages(r))
	body := string(testutil.GetResponseBody(r))
	require.NotContains(t, body, "old private SMS")
	require.Contains(t, body, "current SMS")
}

func TestUnifiedSMSSendDoesNotRequireWhatsAppAccount(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("phone_number", "27820000000").Error)
	contact.PhoneNumber = "27820000000"
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
	require.NoError(t, app.DB.Create(&c).Error)
	app.Channels = &channelapp.Service{DB: app.DB, EncryptionKey: app.Config.App.EncryptionKey}
	msg, err := app.SendOutgoingMessage(context.Background(), handlers.OutgoingMessageRequest{Contact: contact, Channel: "sms", ChannelConnectionID: c.ID, ConsentConfirmed: true, Type: models.MessageTypeText, Content: "Hello SMS"}, handlers.DefaultSendOptions())
	require.NoError(t, err)
	require.Equal(t, "sms", msg.Channel)
	require.Empty(t, msg.WhatsAppAccount)
}

func TestWhatsAppAccountFilterKeepsSMSInUnifiedConversation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	for _, m := range []models.Message{
		{OrganizationID: org.ID, ContactID: contact.ID, Channel: "whatsapp", WhatsAppAccount: "selected", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Selected WA", Status: models.MessageStatusSent},
		{OrganizationID: org.ID, ContactID: contact.ID, Channel: "sms", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Visible SMS", Status: models.MessageStatusSent},
		{OrganizationID: org.ID, ContactID: contact.ID, Channel: "whatsapp", WhatsAppAccount: "other", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Other WA", Status: models.MessageStatusSent},
	} {
		require.NoError(t, app.DB.Create(&m).Error)
	}
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", contact.ID.String())
	r.RequestCtx.QueryArgs().Set("account", "selected")
	require.NoError(t, app.GetMessages(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	body := string(testutil.GetResponseBody(r))
	require.Contains(t, body, "Visible SMS")
	require.Contains(t, body, "Selected WA")
	require.NotContains(t, body, "Other WA")
}

func TestCreateSMSCampaignDoesNotRequireWhatsAppTemplate(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
	require.NoError(t, app.DB.Create(&c).Error)
	app.Channels = &channelapp.Service{DB: app.DB, EncryptionKey: app.Config.App.EncryptionKey}
	r := testutil.NewJSONRequest(t, map[string]any{"name": "SMS campaign", "channel": "sms", "channel_connection_id": c.ID, "sms_body": "Hello", "consent_confirmed": true})
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, app.CreateCampaign(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
}

func TestChannelConfigurationDeniedToOrdinaryAgent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRole(t, app.DB, org.ID, "agent", nil)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, app.ListChannelConnections(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	r = testutil.NewJSONRequest(t, map[string]any{"name": "Unauthorized", "provider": "smsflow", "credentials": map[string]string{"client_secret": "never-echo"}})
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, app.CreateChannelConnection(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	require.NotContains(t, string(testutil.GetResponseBody(r)), "never-echo")
	var count int64
	require.NoError(t, app.DB.Model(&models.ChannelConnection{}).Where("organization_id = ?", org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestChannelHealthIncludesBalanceFailuresAndScopesOrganizations(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	now := time.Now()
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{}, ValidatedAt: &now, NextPollAt: now.Add(time.Minute), BalanceErrorCode: "validation_failed"}
	require.NoError(t, app.DB.Create(&c).Error)
	foreign := c
	foreign.ID = testutil.CreateTestContact(t, app.DB, other.ID).ID
	foreign.OrganizationID = other.ID
	foreign.BalanceErrorCode = ""
	foreign.LowBalance = true
	require.NoError(t, app.DB.Create(&foreign).Error)
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, app.ChannelHealth(r))
	body := string(testutil.GetResponseBody(r))
	require.Contains(t, body, `"healthy":false`)
	require.Contains(t, body, `"balance_failures":1`)
	require.Contains(t, body, `"low_balance_connections":0`)
}

func TestSMSCampaignResumesDurableQueuedRecipients(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	campaign := models.BulkMessageCampaign{OrganizationID: org.ID, Name: "SMS paused", Channel: "sms", CreatedBy: user.ID, Status: models.CampaignStatusPaused}
	require.NoError(t, app.DB.Create(&campaign).Error)
	recipient := models.BulkMessageRecipient{CampaignID: campaign.ID, PhoneNumber: "27820000000", Status: "queued"}
	require.NoError(t, app.DB.Create(&recipient).Error)
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", campaign.ID.String())
	require.NoError(t, app.StartCampaign(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	require.NoError(t, app.DB.First(&campaign, "id = ?", campaign.ID).Error)
	require.Equal(t, models.CampaignStatusProcessing, campaign.Status)
}

func TestCreateSMSBotFlowAndBindConnection(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	graph := map[string]any{"version": 2, "entry_node": "done", "nodes": []any{map[string]any{"id": "done", "type": "end", "config": map[string]any{"message": "Hello"}}}, "edges": []any{}}
	r := testutil.NewJSONRequest(t, map[string]any{"name": "SMS bot", "channel": "sms", "enabled": true, "graph": graph})
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, app.CreateChatbotFlow(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var flow models.ChatbotFlow
	require.NoError(t, app.DB.First(&flow, "organization_id = ? AND name = ?", org.ID, "SMS bot").Error)
	// Check the persisted channel, not just a success response that could ignore it.
	var channel string
	require.NoError(t, app.DB.Model(&flow).Select("channel").Scan(&channel).Error)
	require.Equal(t, "sms", channel)
	now := time.Now()
	connection := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true, "inbound": true}, ValidatedAt: &now}
	require.NoError(t, app.DB.Create(&connection).Error)
	r = testutil.NewJSONRequest(t, map[string]any{"name": "SMS", "enabled": true, "max_segments": 3, "bot_flow_id": flow.ID.String(), "bot_consent_confirmed": true})
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", connection.ID.String())
	require.NoError(t, app.UpdateChannelConnection(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	require.NoError(t, app.DB.First(&connection, "id = ?", connection.ID).Error)
	require.NotNil(t, connection.BotFlowID)
	require.Equal(t, flow.ID, *connection.BotFlowID)
}

func TestStoppingConnectionCancelsPendingWorkButRetainsUncertainSubmissions(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	c := models.ChannelConnection{OrganizationID: org.ID, Name: "SMS", Provider: "fake", Enabled: true, MaxSegments: 3, Credentials: "enc:unused", Capabilities: models.JSONB{"text": true}}
	require.NoError(t, app.DB.Create(&c).Error)
	for _, status := range []string{"queued", "blocked", "unknown"} {
		m := models.Message{OrganizationID: org.ID, ContactID: contact.ID, Channel: "sms", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Hello", Status: models.MessageStatusPending}
		require.NoError(t, app.DB.Create(&m).Error)
		a := models.ChannelAttempt{OrganizationID: org.ID, MessageID: m.ID, LogicalMessageID: m.ID, ConnectionID: &c.ID, Channel: "sms", Status: status}
		require.NoError(t, app.DB.Create(&a).Error)
	}
	r := testutil.NewJSONRequest(t, map[string]any{"name": "SMS", "enabled": false, "max_segments": 3})
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", c.ID.String())
	require.NoError(t, app.UpdateChannelConnection(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var attempts []models.ChannelAttempt
	require.NoError(t, app.DB.Where("connection_id = ?", c.ID).Order("created_at").Find(&attempts).Error)
	require.Len(t, attempts, 3)
	require.Equal(t, "cancelled", attempts[0].Status)
	require.Equal(t, "cancelled", attempts[1].Status)
	require.Equal(t, "unknown", attempts[2].Status)
}
