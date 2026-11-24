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

func TestInboxPersonalisationScopesValuesAndPreservesTypes(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	actor := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin(), testutil.WithFullName("Example agent"))
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550196"))
	contact.ProfileName = "Zoë"
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	other := newInboxCase(inboxThread{org.ID, otherAccount.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	require.NoError(t, db.Create(&other).Error)
	field := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "case", Key: "reference", Label: "Reference", Kind: "text"}
	zero := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "case", Key: "balance", Label: "Balance", Kind: "number"}
	falseField := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "contact", Key: "agreed", Label: "Agreed", Kind: "boolean"}
	require.NoError(t, db.Create(&[]models.InboxFieldDefinition{field, zero, falseField}).Error)
	for _, v := range []models.InboxFieldValue{
		{OrganizationID: org.ID, DefinitionID: field.ID, SubjectID: c.ID, Value: json.RawMessage(`"THIS-CASE"`)},
		{OrganizationID: org.ID, DefinitionID: field.ID, SubjectID: other.ID, Value: json.RawMessage(`"OTHER-ACCOUNT"`)},
		{OrganizationID: org.ID, DefinitionID: zero.ID, SubjectID: c.ID, Value: json.RawMessage(`0`)},
		{OrganizationID: org.ID, DefinitionID: falseField.ID, SubjectID: contact.ID, Value: json.RawMessage(`false`)},
	} {
		require.NoError(t, db.Create(&v).Error)
	}
	rendered, err := a.renderInboxMessage(db, c, *account, *contact, actor.ID, `Hi {{contact_name}}, {{case.field.reference}}: {{case.field.balance}} / {{contact.field.agreed}} — {{agent_name}}`)
	require.NoError(t, err)
	require.Empty(t, rendered.Missing)
	require.Equal(t, "Hi Zoë, THIS-CASE: 0 / false — Example agent", rendered.Text)
	_, err = a.renderInboxMessage(db, c, *otherAccount, *contact, actor.ID, `{{case.field.reference}}`)
	require.ErrorIs(t, err, errInboxTransition)
	otherOrg := testutil.CreateTestOrganization(t, db)
	foreignContact := testutil.CreateTestContact(t, db, otherOrg.ID)
	_, err = a.renderInboxMessage(db, c, *account, *foreignContact, actor.ID, `{{contact_name}}`)
	require.ErrorIs(t, err, errInboxTransition)
	// An actor without contact-read rights cannot obtain contact field values,
	// including by authoring an otherwise valid key with a fallback.
	limitedRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "Preview without contact fields", []string{"chat:read"})
	limited := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&limitedRole.ID))
	blocked, err := a.renderInboxMessage(db, c, *account, *contact, limited.ID, `{{contact.field.agreed | "yes"}}`)
	require.NoError(t, err)
	require.Len(t, blocked.Missing, 1)
	require.Equal(t, "unavailable", blocked.Missing[0].Reason)
}

func TestInboxPersonalisationMaskingArchivesAndLegacyText(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550197"))
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Model(org).Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)
	rendered, err := a.renderInboxMessage(db, c, *account, *contact, uuid.Nil, `{{phone_number}}`)
	require.NoError(t, err)
	require.Len(t, rendered.Missing, 1)
	require.NotContains(t, rendered.Text, contact.PhoneNumber)
	rendered, err = a.renderInboxMessage(db, c, *account, *contact, uuid.Nil, `{{phone_number | "your number"}}`)
	require.NoError(t, err)
	require.Equal(t, "your number", rendered.Text)
	archived := time.Now().UTC()
	definition := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "case", Key: "retired", Label: "Retired", Kind: "text", ArchivedAt: &archived}
	require.NoError(t, db.Create(&definition).Error)
	rendered, err = a.renderInboxMessage(db, c, *account, *contact, uuid.Nil, `{{case.field.retired}}`)
	require.NoError(t, err)
	require.Equal(t, "archived", rendered.Missing[0].Reason)
	require.NoError(t, db.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText}).Error)
	legacy := models.InboxAction{Type: "send_text", Value: `Literal {{contact_name}}`}
	preview, err := a.previewInboxReply(c, *account, *contact, legacy, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, legacy.Value, preview.Text)
	legacy.Personalise, legacy.Value = true, `Hello {{phone_number}}`
	preview, err = a.previewInboxReply(c, *account, *contact, legacy, time.Now().UTC())
	require.Equal(t, "variables_missing", inboxEligibilityReason(err))
	require.Len(t, preview.Missing, 1)
}

func TestInboxTemplatePersonalisationParametersAndFingerprint(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550198"))
	contact.ProfileName = "Example customer"
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	template := models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, WhatsAppAccount: account.Name, Name: "case_update", Language: "en", Status: "APPROVED", Category: "UTILITY", HeaderType: "TEXT", HeaderContent: "Case {{1}}", BodyContent: "Hello {{1}}, status {{2}}."}
	require.NoError(t, db.Create(&template).Error)
	action := models.InboxAction{Type: "send_template", Value: template.ID.String(), ConsentConfirmed: true, Personalise: true,
		BodyParameters: map[string]string{"1": "{{contact_name}}", "2": "{{case_status}}"}, HeaderParameters: map[string]string{"1": "Reference"}}
	preview, err := a.previewInboxReply(c, *account, *contact, action, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "Hello Example customer, status open.", preview.Text)
	require.Equal(t, "Case Reference", preview.Header)
	require.Equal(t, "Example customer", preview.BodyParameters["1"])
	require.Equal(t, "Reference", preview.HeaderParameters["1"])
	digest := inboxReplyFingerprint(c, *account, *contact, preview)
	contact.ProfileName = "Changed name"
	changed, err := a.previewInboxReply(c, *account, *contact, action, time.Now().UTC())
	require.NoError(t, err)
	require.NotEqual(t, digest, inboxReplyFingerprint(c, *account, *contact, changed))
	delete(action.HeaderParameters, "1")
	_, err = a.previewInboxReply(c, *account, *contact, action, time.Now().UTC())
	require.Equal(t, "template_parameters_missing", inboxEligibilityReason(err))
	action.HeaderParameters["1"] = "Reference"
	require.NoError(t, db.Model(&template).Update("status", "PAUSED").Error)
	_, err = a.previewInboxReply(c, *account, *contact, action, time.Now().UTC())
	require.Equal(t, "template_not_approved", inboxEligibilityReason(err))
	// Database errors remain operational errors, not a false missing-variable
	// success. The caller cannot confirm or dispatch an incomplete preview.
	err = db.Transaction(func(tx *gorm.DB) error {
		broken := tx.Session(&gorm.Session{}).Where("1 = (SELECT 1 / 0)")
		_, err := a.renderInboxMessage(broken, c, *account, *contact, uuid.Nil, "{{contact_name}}")
		require.Error(t, err)
		return err
	})
	require.Error(t, err)
}

func TestInboxPersonalisationValidatesAuthoredSyntax(t *testing.T) {
	base := models.InboxWorkflow{Name: "Personalised reply", Kind: "macro", Actions: []models.InboxAction{{Type: "send_text", Value: "Hello {{contact_name}}", Personalise: true}}}
	require.True(t, validInboxWorkflow(base))
	base.Actions[0].Value = "{{access_token}}"
	require.False(t, validInboxWorkflow(base))
	base.Actions[0].Personalise = false
	require.True(t, validInboxWorkflow(base))
	base.Actions[0].Type = "note"
	base.Actions[0].Personalise = true
	require.False(t, validInboxWorkflow(base))
}

func TestInboxPersonalisationPreviewConfirmationAndCatalogue(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	actor := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550194"))
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(contact).Update("profile_name", "First name").Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	require.NoError(t, db.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText}).Error)
	r := testutil.NewJSONRequest(t, map[string]any{"case_id": c.ID, "text": "Hi {{contact_name}}"})
	r.RequestCtx.SetUserValue("organization_id", org.ID)
	r.RequestCtx.SetUserValue("user_id", actor.ID)
	require.NoError(t, a.PreviewInboxMessage(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var result struct {
		CaseID       uuid.UUID         `json:"case_id"`
		Version      int64             `json:"case_version"`
		Confirmation string            `json:"confirmation"`
		Reply        inboxReplyPreview `json:"reply"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	require.Equal(t, "Hi First name", result.Reply.Text)
	require.Len(t, result.Confirmation, 64)
	confirmed := inboxMessageConfirmation{CaseID: result.CaseID, Version: result.Version, AuthoredText: "Hi {{contact_name}}", Confirmation: result.Confirmation}
	text, err := a.confirmInboxMessage(db, org.ID, actor.ID, *account, *contact, confirmed)
	require.NoError(t, err)
	require.Equal(t, result.Reply.Text, text)
	forged := confirmed
	forged.Confirmation = workflowDigest([]any{actor.ID, c.ID, c.Version, confirmed.AuthoredText, inboxReplyFingerprint(c, *account, *contact, result.Reply)})
	_, err = a.confirmInboxMessage(db, org.ID, actor.ID, *account, *contact, forged)
	require.ErrorIs(t, err, errInboxConflict)
	require.NoError(t, db.Model(contact).Update("profile_name", "Changed name").Error)
	require.NoError(t, db.First(contact, "id=?", contact.ID).Error)
	_, err = a.confirmInboxMessage(db, org.ID, actor.ID, *account, *contact, confirmed)
	require.ErrorIs(t, err, errInboxConflict)
	confirmed.Version++
	_, err = a.confirmInboxMessage(db, org.ID, actor.ID, *account, *contact, confirmed)
	require.ErrorIs(t, err, errInboxConflict)
	unauthenticated := testutil.NewGETRequest(t)
	require.NoError(t, a.ListInboxMessageVariables(unauthenticated))
	require.Equal(t, 401, testutil.GetResponseStatusCode(unauthenticated))
	require.NoError(t, db.Model(org).Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)
	catalogue := testutil.NewGETRequest(t)
	catalogue.RequestCtx.SetUserValue("organization_id", org.ID)
	catalogue.RequestCtx.SetUserValue("user_id", actor.ID)
	require.NoError(t, a.ListInboxMessageVariables(catalogue))
	var definitions struct {
		Variables []inboxMessageVariable `json:"variables"`
	}
	testutil.ParseEnvelopeResponse(t, catalogue, &definitions)
	for _, variable := range definitions.Variables {
		require.NotEqual(t, "phone_number", variable.Key)
	}
	require.NotContains(t, string(testutil.GetResponseBody(catalogue)), contact.PhoneNumber)
}

func TestInboxPersonalisationManualSendRevalidatesAndDoesNotDuplicate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	rdb := testutil.SetupTestRedis(t)
	require.NotNil(t, rdb)
	a := &App{DB: db, Redis: rdb, Log: testutil.NopLogger(), Config: &config.Config{}}
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
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"messages":[{"id":"wamid.personalisation.synthetic"}]}`))}, nil
	})}
	org := testutil.CreateTestOrganization(t, db)
	actor := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550193"))
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(contact).Update("profile_name", "Synthetic customer").Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	require.NoError(t, db.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText}).Error)
	require.NoError(t, db.First(contact, "id=?", contact.ID).Error)
	action := models.InboxAction{Type: "send_text", Value: "Hello {{contact_name}}", Personalise: true}
	preview, err := a.previewInboxReplyForActor(db, c, *account, *contact, action, time.Now().UTC(), actor.ID)
	require.NoError(t, err)
	confirmation := inboxMessageConfirmation{CaseID: c.ID, Version: c.Version, AuthoredText: action.Value, Confirmation: inboxMessageConfirmationDigest(c, *account, *contact, actor.ID, action.Value, preview)}
	id := uuid.New()
	send := func(body string) int {
		r := testutil.NewJSONRequest(t, map[string]any{"type": "text", "whatsapp_account": account.Name, "client_message_id": id, "content": map[string]string{"body": body}, "personalisation": confirmation})
		r.RequestCtx.SetUserValue("organization_id", org.ID)
		r.RequestCtx.SetUserValue("user_id", actor.ID)
		r.RequestCtx.SetUserValue("id", contact.ID.String())
		require.NoError(t, a.SendMessage(r))
		return testutil.GetResponseStatusCode(r)
	}
	// A client cannot swap a body after reviewing it.
	require.Equal(t, 409, send("Unreviewed text"))
	require.Zero(t, calls.Load())
	require.Equal(t, 200, send(preview.Text))
	select {
	case payload := <-payloads:
		require.Contains(t, payload, "Hello Synthetic customer")
		require.NotContains(t, payload, "{{contact_name}}")
	case <-time.After(3 * time.Second):
		t.Fatal("fake provider was not called")
	}
	require.Eventually(t, func() bool {
		var count int64
		return db.Model(&models.Message{}).Where("id=? AND status=?", id, models.MessageStatusSent).Count(&count).Error == nil && count == 1
	}, 3*time.Second, 10*time.Millisecond)
	// A replay either returns the durable result or rejects a changed case;
	// neither outcome resubmits to the provider.
	status := send(preview.Text)
	require.Contains(t, []int{200, 409}, status)
	require.EqualValues(t, 1, calls.Load())
	var count int64
	require.NoError(t, db.Model(&models.Message{}).Where("id=?", id).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxPersonalisationPreviewDoesNotBroadenAssignedAccountAccess(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "Assigned personalisation", []string{"chat:read", "chat:write"})
	agent := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	other := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	assigned := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), &agent.ID, nil)
	unassigned := newInboxCase(inboxThread{org.ID, other.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&[]models.InboxCase{assigned, unassigned}).Error)
	for _, item := range []struct {
		c        models.InboxCase
		expected int
	}{{assigned, 200}, {unassigned, 404}} {
		r := testutil.NewJSONRequest(t, map[string]any{"case_id": item.c.ID, "text": "Hello {{contact_name}}"})
		testutil.SetAuthContext(r, org.ID, agent.ID)
		require.NoError(t, a.PreviewInboxMessage(r))
		require.Equal(t, item.expected, testutil.GetResponseStatusCode(r))
	}
}

func TestInboxPersonalisationPrivacyLookupFailsClosed(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	err := db.Transaction(func(tx *gorm.DB) error {
		mask, err := inboxMessageMasking(tx.Where("1 = (SELECT 1 / 0)"), org.ID)
		require.True(t, mask)
		require.Error(t, err)
		return err
	})
	require.Error(t, err)
}
