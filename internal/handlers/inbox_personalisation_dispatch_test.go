package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxPersonalisedTemplateDispatchAndRestart(t *testing.T) {
	db, redis := testutil.SetupTestDB(t), testutil.SetupTestRedis(t)
	require.NotNil(t, redis)
	a := &App{DB: db, Redis: redis, Log: testutil.NopLogger(), Config: &config.Config{InboxAutomation: config.InboxAutomationConfig{DispatchEnabled: true}}}
	a.WhatsApp = whatsapp.NewWithTimeout(a.Log, time.Second)
	var calls atomic.Int32
	payload := ""
	a.WhatsApp.HTTPClient = &http.Client{Transport: inboxFixtureTransport(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		payload = string(body)
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"messages":[{"id":"wamid.personalised.template.synthetic"}]}`))}, nil
	})}
	org := testutil.CreateTestOrganization(t, db)
	actor := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550192"))
	require.NoError(t, db.Model(org).Updates(map[string]any{"inbox_workflows_enabled": true, "inbox_automation_sends_enabled": true}).Error)
	require.NoError(t, db.Model(contact).Update("profile_name", "Example {{2}}").Error)
	require.NoError(t, db.First(contact, "id=?", contact.ID).Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	template := models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, WhatsAppAccount: account.Name, Name: "case_update", Language: "en", Status: "APPROVED", Category: "UTILITY", HeaderType: "TEXT", HeaderContent: "Case {{1}}", BodyContent: "Hello {{1}}; state {{2}}"}
	require.NoError(t, db.Create(&template).Error)
	w := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: actor.ID, Name: "Personalised utility", Kind: "macro", Visibility: "organization", Actions: []models.InboxAction{{Type: "send_template", Value: template.ID.String(), ConsentConfirmed: true, Personalise: true, HeaderParameters: map[string]string{"1": "HEADER"}, BodyParameters: map[string]string{"1": "{{contact_name}}", "2": "Open"}}}}
	require.NoError(t, db.Create(&w).Error)
	queue := func() models.InboxWorkflowRun {
		latest, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
		require.NoError(t, err)
		require.NoError(t, db.First(contact, "id=?", contact.ID).Error)
		preview, err := a.evaluateInboxWorkflow(db, w, latest, *account, *contact, actor.ID, time.Now().UTC())
		require.NoError(t, err)
		require.False(t, preview.SendUnavailable)
		run := newInboxWorkflowRun(uuid.New(), w, latest, actor.ID)
		run.State, run.SendDigest = inboxRunPending, preview.Digest
		require.NoError(t, saveInboxWorkflowRun(db, latest, &run))
		return run
	}
	run := queue()
	claimed, ok, err := a.claimInboxSend(context.Background(), run.ID, org.ID, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, ok)
	state, outcome := a.dispatchClaimedInboxSend(context.Background(), claimed)
	require.Equal(t, inboxRunSent, state)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return finishInboxSend(tx, claimed, state, outcome) }))
	require.Contains(t, payload, "HEADER")
	require.Contains(t, payload, "Example {{2}}")
	require.NotContains(t, payload, "{{contact_name}}")
	var stored models.Message
	require.NoError(t, db.First(&stored, "id=?", run.ID).Error)
	require.Equal(t, "Hello Example {{2}}; state Open", stored.Content)
	resumed := &App{DB: db, Redis: redis, Log: a.Log, Config: a.Config, WhatsApp: a.WhatsApp}
	_, replay, err := resumed.claimInboxSend(context.Background(), run.ID, org.ID, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, replay)
	require.EqualValues(t, 1, calls.Load())
	changed := queue()
	require.NoError(t, db.Model(contact).Update("profile_name", "Changed after confirmation").Error)
	claimed, ok, err = resumed.claimInboxSend(context.Background(), changed.ID, org.ID, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, ok)
	state, outcome = resumed.dispatchClaimedInboxSend(context.Background(), claimed)
	require.Equal(t, inboxRunBlocked, state)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return finishInboxSend(tx, claimed, state, outcome) }))
	require.EqualValues(t, 1, calls.Load())
}
