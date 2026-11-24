package handlers

import (
	"encoding/json"
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

func cannedPersonalisationFixture(t *testing.T) (*App, models.InboxCase, *models.WhatsAppAccount, *models.Contact, *models.User, *models.CannedResponse) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger(), Config: &config.Config{}}
	org := testutil.CreateTestOrganization(t, db)
	actor := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550191"))
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(contact).Update("profile_name", "Zoë").Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	require.NoError(t, db.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText}).Error)
	saved := &models.CannedResponse{OrganizationID: org.ID, Name: "Synthetic welcome", Content: "Hello {{contact_name}}", Personalise: true, IsActive: true, CreatedByID: actor.ID}
	require.NoError(t, db.Create(saved).Error)
	return a, c, account, contact, actor, saved
}

func TestInboxCannedPersonalisationAuthoredValidation(t *testing.T) {
	valid := []CannedResponseButton{{ID: "yes", Type: "reply", Title: `{{contact_name | "Yes"}}`}}
	require.NoError(t, validatePersonalisedCanned("Hi {{contact_name}}", valid, true))
	for _, buttons := range [][]CannedResponseButton{
		{{ID: "{{case_id}}", Title: "Yes"}},
		{{ID: "yes", Title: "{{access_token}}"}},
		{{ID: "yes", Title: "Yes", Type: "unknown"}},
		{{ID: "yes", Title: "Phone", Type: "phone"}},
		{{ID: "", Title: "Yes"}},
		{{ID: "yes", Title: "Open", Type: "flow", FlowID: "{{case_id}}"}},
	} {
		require.Error(t, validatePersonalisedCanned("Hello", buttons, true))
	}
	// Legacy manual placeholders remain authored literal text, not new variables.
	require.NoError(t, validatePersonalisedCanned("Hi {{custom_manual_name}}", nil, false))
	require.Error(t, validatePersonalisedCanned("{{custom_manual_name}}", nil, true))
	require.Error(t, validatePersonalisedCanned(strings.Repeat("{{case_id}}", 65), nil, true))
}

func TestInboxCannedPersonalisationRichPreviewAndLimits(t *testing.T) {
	a, c, account, contact, actor, saved := cannedPersonalisationFixture(t)
	preview := func(buttons []CannedResponseButton) (inboxReplyPreview, error) {
		require.NoError(t, a.DB.Model(saved).Update("buttons", buttonsToJSONBArray(buttons)).Error)
		r, _, err := a.previewPersonalisedCanned(a.DB, c, *account, *contact, actor.ID, saved.ID, time.Now().UTC())
		return r, err
	}
	for _, count := range []int{1, 3, 4, 10} {
		buttons := make([]CannedResponseButton, count)
		for i := range buttons {
			buttons[i] = CannedResponseButton{ID: uuid.NewString(), Type: "reply", Title: "Hi {{contact_name}}"}
		}
		r, err := preview(buttons)
		require.NoError(t, err)
		require.Equal(t, "Hello Zoë", r.Text)
		require.Len(t, r.Interactive.Buttons, count)
		require.Equal(t, "Hi Zoë", r.Interactive.Buttons[0].Title)
		if count > 3 {
			require.Equal(t, "list", r.Interactive.Type)
		} else {
			require.Equal(t, "button", r.Interactive.Type)
		}
	}
	r, err := preview([]CannedResponseButton{{Type: "url", Title: "View {{contact_name}}", URL: "https://example.invalid/cases/{{case_id}}"}})
	require.NoError(t, err)
	require.Equal(t, "cta_url", r.Interactive.Type)
	require.Equal(t, "https://example.invalid/cases/"+c.ID.String(), r.Interactive.URL)
	_, err = preview([]CannedResponseButton{{Type: "voice_call", Title: "Call {{contact_name}}", TTLMinutes: 15}})
	require.Equal(t, "calling_unavailable", inboxEligibilityReason(err))
	account.BusinessCallingEnabled = true
	r, err = preview([]CannedResponseButton{{Type: "voice_call", Title: "Call {{contact_name}}", TTLMinutes: 15}})
	require.NoError(t, err)
	require.Equal(t, 15, r.Interactive.TTLMinutes)
	flow := models.WhatsAppFlow{OrganizationID: c.OrganizationID, WhatsAppAccount: account.Name, MetaFlowID: "synthetic-flow", Name: "Synthetic flow", Status: "PUBLISHED"}
	require.NoError(t, a.DB.Create(&flow).Error)
	r, err = preview([]CannedResponseButton{{Type: "flow", Title: "Open {{contact_name}}", FlowID: flow.MetaFlowID, Screen: "START"}})
	require.NoError(t, err)
	require.Equal(t, "START", r.Interactive.FirstScreen)
	require.NoError(t, a.DB.Model(&flow).Update("whats_app_account", "another-account").Error)
	_, err = preview([]CannedResponseButton{{Type: "flow", Title: "Open", FlowID: flow.MetaFlowID}})
	require.Equal(t, "flow_unavailable", inboxEligibilityReason(err))
	require.NoError(t, a.DB.Model(contact).Update("profile_name", strings.Repeat("界", 21)).Error)
	require.NoError(t, a.DB.First(contact, "id=?", contact.ID).Error)
	r, err = preview([]CannedResponseButton{{ID: "answer", Title: "{{contact_name}}"}})
	require.Equal(t, "canned_content_invalid", inboxEligibilityReason(err))
	require.Equal(t, strings.Repeat("界", 21), r.Interactive.Buttons[0].Title)
	// Corrupt legacy JSON must not silently turn an interactive reply into text.
	require.NoError(t, a.DB.Model(saved).Update("buttons", models.JSONBArray{"invalid"}).Error)
	_, _, err = a.previewPersonalisedCanned(a.DB, c, *account, *contact, actor.ID, saved.ID, time.Now().UTC())
	require.Equal(t, "canned_content_invalid", inboxEligibilityReason(err))
}

func TestInboxCannedPersonalisationScopeAndRevision(t *testing.T) {
	a, c, account, contact, actor, saved := cannedPersonalisationFixture(t)
	r, authored, err := a.previewPersonalisedCanned(a.DB, c, *account, *contact, actor.ID, saved.ID, time.Now().UTC())
	require.NoError(t, err)
	input := inboxMessageConfirmation{CaseID: c.ID, Version: c.Version, CannedResponseID: &saved.ID, Confirmation: inboxMessageConfirmationDigest(c, *account, *contact, actor.ID, authored, r)}
	_, err = a.confirmInboxReply(a.DB, c.OrganizationID, actor.ID, *account, *contact, input)
	require.NoError(t, err)
	require.NoError(t, a.DB.Model(saved).Update("name", "Changed definition").Error)
	_, err = a.confirmInboxReply(a.DB, c.OrganizationID, actor.ID, *account, *contact, input)
	require.ErrorIs(t, err, errInboxConflict)
	role := testutil.CreateTestRoleWithKeys(t, a.DB, c.OrganizationID, "No saved reply read", []string{"chat:read", "chat:write"})
	limited := testutil.CreateTestUser(t, a.DB, c.OrganizationID, testutil.WithRoleID(&role.ID))
	_, _, err = a.previewPersonalisedCanned(a.DB, c, *account, *contact, limited.ID, saved.ID, time.Now().UTC())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	foreign := testutil.CreateTestOrganization(t, a.DB)
	require.NoError(t, a.DB.Model(saved).Update("organization_id", foreign.ID).Error)
	_, _, err = a.previewPersonalisedCanned(a.DB, c, *account, *contact, actor.ID, saved.ID, time.Now().UTC())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, a.DB.Model(saved).Updates(map[string]any{"organization_id": c.OrganizationID, "is_active": false}).Error)
	_, _, err = a.previewPersonalisedCanned(a.DB, c, *account, *contact, actor.ID, saved.ID, time.Now().UTC())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestInboxCannedPersonalisationCreateEditAndCopy(t *testing.T) {
	a, c, _, _, actor, _ := cannedPersonalisationFixture(t)
	buttons := []CannedResponseButton{{ID: "yes", Title: `{{contact_name | "Customer"}}`}}
	create := func(name string) CannedResponseResponse {
		r := testutil.NewJSONRequest(t, map[string]any{"name": name, "content": "Hello {{contact_name}}", "personalise": true, "buttons": buttons})
		testutil.SetAuthContext(r, c.OrganizationID, actor.ID)
		require.NoError(t, a.CreateCannedResponse(r))
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		var saved CannedResponseResponse
		testutil.ParseEnvelopeResponse(t, r, &saved)
		require.True(t, saved.Personalise)
		require.Equal(t, buttons, saved.Buttons)
		return saved
	}
	saved := create("Synthetic saved reply")
	copy := create("Synthetic saved reply copy")
	require.NotEqual(t, saved.ID, copy.ID)
	// Old clients that omit the additive flag do not silently turn it off.
	r := testutil.NewJSONRequest(t, map[string]any{"name": saved.Name, "content": saved.Content, "buttons": saved.Buttons, "is_active": true})
	testutil.SetAuthContext(r, c.OrganizationID, actor.ID)
	r.RequestCtx.SetUserValue("id", saved.ID.String())
	require.NoError(t, a.UpdateCannedResponse(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var updated CannedResponseResponse
	testutil.ParseEnvelopeResponse(t, r, &updated)
	require.True(t, updated.Personalise)
	// Disabling restores manually entered placeholders without rewriting them.
	r = testutil.NewJSONRequest(t, map[string]any{"content": "Hello {{manual_name}}", "personalise": false, "is_active": true})
	testutil.SetAuthContext(r, c.OrganizationID, actor.ID)
	r.RequestCtx.SetUserValue("id", saved.ID.String())
	require.NoError(t, a.UpdateCannedResponse(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	testutil.ParseEnvelopeResponse(t, r, &updated)
	// omitempty intentionally omits false in the response; inspect persistence.
	var stored models.CannedResponse
	require.NoError(t, a.DB.First(&stored, "id=?", saved.ID).Error)
	require.False(t, stored.Personalise)
	require.Equal(t, "Hello {{manual_name}}", stored.Content)
}

func TestInboxCannedPersonalisationLegacyColumnMigration(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE TEMP TABLE inbox_canned_migration_fixture (id uuid PRIMARY KEY, content text NOT NULL) ON COMMIT DROP").Error; err != nil {
			return err
		}
		id := uuid.New()
		if err := tx.Exec("INSERT INTO inbox_canned_migration_fixture (id,content) VALUES (?,?)", id, "Hello {{manual_name}}").Error; err != nil {
			return err
		}
		if err := tx.Table("inbox_canned_migration_fixture").Migrator().AddColumn(&models.CannedResponse{}, "Personalise"); err != nil {
			return err
		}
		var restored struct {
			Content     string
			Personalise bool
		}
		if err := tx.Table("inbox_canned_migration_fixture").Where("id=?", id).First(&restored).Error; err != nil {
			return err
		}
		require.False(t, restored.Personalise)
		require.Equal(t, "Hello {{manual_name}}", restored.Content)
		return nil
	}))
}

func TestInboxCannedPersonalisationReviewedSendAndReplay(t *testing.T) {
	a, c, account, contact, actor, saved := cannedPersonalisationFixture(t)
	a.Redis = testutil.SetupTestRedis(t)
	require.NotNil(t, a.Redis)
	a.WhatsApp = whatsapp.NewWithTimeout(a.Log, time.Second)
	var calls atomic.Int32
	payloads := make(chan string, 2)
	a.WhatsApp.HTTPClient = &http.Client{Transport: inboxFixtureTransport(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		calls.Add(1)
		payloads <- string(body)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"messages":[{"id":"wamid.canned.synthetic"}]}`))}, nil
	})}
	require.NoError(t, a.DB.Model(saved).Update("buttons", buttonsToJSONBArray([]CannedResponseButton{{ID: "yes", Title: "Hi {{contact_name}}"}})).Error)
	request := testutil.NewJSONRequest(t, map[string]any{"case_id": c.ID, "canned_response_id": saved.ID})
	testutil.SetAuthContext(request, c.OrganizationID, actor.ID)
	require.NoError(t, a.PreviewInboxMessage(request))
	require.Equal(t, 200, testutil.GetResponseStatusCode(request))
	var preview struct {
		Reply        inboxReplyPreview `json:"reply"`
		Confirmation string            `json:"confirmation"`
	}
	testutil.ParseEnvelopeResponse(t, request, &preview)
	require.Len(t, preview.Confirmation, 64)
	confirmation := inboxMessageConfirmation{CaseID: c.ID, Version: c.Version, CannedResponseID: &saved.ID, Confirmation: preview.Confirmation}
	id := uuid.New()
	send := func(interactive *InteractiveContent) int {
		r := testutil.NewJSONRequest(t, map[string]any{"type": "interactive", "whatsapp_account": account.Name, "client_message_id": id, "content": map[string]string{"body": preview.Reply.Text}, "interactive": interactive, "personalisation": confirmation})
		testutil.SetAuthContext(r, c.OrganizationID, actor.ID)
		r.RequestCtx.SetUserValue("id", contact.ID.String())
		require.NoError(t, a.SendMessage(r))
		return testutil.GetResponseStatusCode(r)
	}
	tampered := *preview.Reply.Interactive
	tampered.Header = "Not reviewed"
	require.Equal(t, 409, send(&tampered))
	require.Equal(t, 409, send(nil))
	require.Zero(t, calls.Load())
	require.Equal(t, 200, send(preview.Reply.Interactive))
	select {
	case payload := <-payloads:
		var sent map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &sent))
		require.Contains(t, payload, "Hello Zoë")
		require.Contains(t, payload, "Hi Zoë")
		require.NotContains(t, payload, "{{contact_name}}")
	case <-time.After(3 * time.Second):
		t.Fatal("fake provider was not called")
	}
	require.Eventually(t, func() bool {
		var count int64
		return a.DB.Model(&models.Message{}).Where("id=? AND status=?", id, models.MessageStatusSent).Count(&count).Error == nil && count == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.Contains(t, []int{200, 409}, send(preview.Reply.Interactive))
	require.EqualValues(t, 1, calls.Load())
	var count int64
	require.NoError(t, a.DB.Model(&models.Message{}).Where("id=?", id).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
