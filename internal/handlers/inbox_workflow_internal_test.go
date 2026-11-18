package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
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
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550196"))
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
	templateAction := models.InboxAction{Type: "send_template", Value: template.ID.String(), ConsentConfirmed: true}
	preview, err = app.previewInboxReply(c, *account, *contact, templateAction, now.Add(48*time.Hour))
	require.NoError(t, err)
	require.Equal(t, template.BodyContent, preview.Text)
	require.NoError(t, db.Model(&template).Update("status", "PAUSED").Error)
	_, err = app.previewInboxReply(c, *account, *contact, templateAction, now)
	require.ErrorIs(t, err, errInboxSendIneligible)
}

type inboxFixtureTransport func(*http.Request) (*http.Response, error)

func (f inboxFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestInboxRulesSelectOneSendAndDoNotReplayUnknown(t *testing.T) {
	db := testutil.SetupTestDB(t)
	rdb := testutil.SetupTestRedis(t)
	require.NotNil(t, rdb)
	log := testutil.NopLogger()
	a := &App{DB: db, Redis: rdb, Log: log, Config: &config.Config{InboxAutomation: config.InboxAutomationConfig{DispatchEnabled: true}}}
	var calls atomic.Int32
	a.WhatsApp = whatsapp.NewWithTimeout(log, time.Second)
	a.WhatsApp.HTTPClient = &http.Client{Transport: inboxFixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, "POST", req.Method)
		payload, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Contains(t, string(payload), "Only first rule may send")
		require.Contains(t, string(payload), "12025550195")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("{\"messages\":[{\"id\":\"wamid.workflow.fixture\"}]}"))}, nil
	})}
	org := testutil.CreateTestOrganization(t, db)
	operator := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	require.NoError(t, db.Model(org).Updates(map[string]any{"inbox_workflows_enabled": true, "inbox_automation_sends_enabled": true}).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550195"))
	enabledAt := time.Now().UTC().Add(-time.Hour)
	primary := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: operator.ID, Name: "First", Kind: "rule", Trigger: "incoming_message", EnabledAt: &enabledAt, Position: 1, Actions: []models.InboxAction{{Type: "send_text", Value: "Only first rule may send"}}}
	secondary := primary
	secondary.BaseModel = models.BaseModel{ID: uuid.New()}
	secondary.Name = "Second"
	secondary.Position = 2
	secondary.Actions = []models.InboxAction{{Type: "send_text", Value: "Second rule"}}
	require.NoError(t, db.Create(&primary).Error)
	require.NoError(t, db.Create(&secondary).Error)
	at := time.Now().UTC().Add(-time.Second)
	require.NoError(t, db.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: at}, OrganizationID: org.ID,
		WhatsAppAccount: account.Name, ContactID: contact.ID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText}).Error)
	a.recordInboxInbound(account, contact, "one", at, nil)
	var event models.InboxCaseActivity
	require.NoError(t, db.First(&event, "organization_id = ? AND event_key = ?", org.ID, "one").Error)
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	var runs []models.InboxWorkflowRun
	require.NoError(t, db.Order("created_at,id").Find(&runs, "organization_id = ?", org.ID).Error)
	require.Len(t, runs, 2)
	require.Equal(t, inboxRunPending, runs[0].State)
	require.Equal(t, "event_send_already_selected", runs[1].Outcome)
	_, held, err := a.claimInboxSend(context.Background(), runs[0].ID, org.ID, time.Now().UTC().Add(-3*time.Minute))
	require.NoError(t, err)
	require.True(t, held)
	resumed := &App{DB: db, Redis: rdb, Log: log, Config: a.Config, WhatsApp: a.WhatsApp}
	require.NoError(t, resumed.runInboxWorkflowCycle(context.Background()))
	require.NoError(t, db.First(&runs[0], "id = ?", runs[0].ID).Error)
	require.Equal(t, inboxRunUnknown, runs[0].State)
	require.Zero(t, calls.Load())

	a.recordInboxInbound(account, contact, "two", time.Now().UTC(), nil)
	require.NoError(t, resumed.runInboxWorkflowCycle(context.Background()))
	require.NoError(t, resumed.runInboxWorkflowCycle(context.Background()))
	require.EqualValues(t, 1, calls.Load())
	var sent int64
	require.NoError(t, db.Model(&models.InboxWorkflowRun{}).Where("organization_id = ? AND state = ?", org.ID, inboxRunSent).Count(&sent).Error)
	require.EqualValues(t, 1, sent)
}
