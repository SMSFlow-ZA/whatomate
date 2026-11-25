package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxIntakeConfigurationDefaultsDisabledAndUsesRevisionCheckedUpdates(t *testing.T) {
	f := newIntakeBindingFixture(t)
	input := inboxIntakeMappingInput{Name: "Another synthetic mapping", ConsentKey: "consent", Enabled: true, Fields: f.mapping.Fields}
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	require.NoError(t, f.a.CreateInboxIntakeMapping(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	var saved models.InboxIntakeMapping
	require.NoError(t, f.db.First(&saved, "organization_id=? AND name=?", f.org.ID, input.Name).Error)
	require.False(t, saved.Enabled, "creation never enables collection implicitly")
	input.Revision = saved.Revision
	update := func() int {
		r := testutil.NewJSONRequest(t, input)
		testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
		testutil.SetPathParam(r, "mapping_id", saved.ID.String())
		require.NoError(t, f.a.UpdateInboxIntakeMapping(r))
		return r.RequestCtx.Response.StatusCode()
	}
	require.Equal(t, 200, update())
	require.Equal(t, 409, update(), "a second stale definition cannot overwrite the first")
	// An archived target must not prevent the emergency disable operation.
	require.NoError(t, f.db.Model(&f.field).Update("archived_at", time.Now().UTC()).Error)
	disable := testutil.NewJSONRequest(t, map[string]any{"revision": saved.Revision + 1})
	testutil.SetAuthContext(disable, f.org.ID, f.admin.ID)
	testutil.SetPathParam(disable, "mapping_id", saved.ID.String())
	require.NoError(t, f.a.DisableInboxIntakeMapping(disable))
	require.Equal(t, 200, disable.RequestCtx.Response.StatusCode())
	saved = models.InboxIntakeMapping{}
	require.NoError(t, f.db.First(&saved, "organization_id=? AND name=?", f.org.ID, input.Name).Error)
	require.False(t, saved.Enabled)
	require.EqualValues(t, 3, saved.Revision)
}

func TestInboxIntakeConfigurationRejectsForeignFieldsAndForgedAuthority(t *testing.T) {
	f := newIntakeBindingFixture(t)
	foreign := testutil.CreateTestOrganization(t, f.db)
	field := models.InboxFieldDefinition{OrganizationID: foreign.ID, Scope: "case", Key: "foreign", Label: "Foreign", Kind: "text"}
	require.NoError(t, f.db.Create(&field).Error)
	input := inboxIntakeMappingInput{Name: "Foreign destination", ConsentKey: "consent", Fields: []models.InboxIntakeInput{{Input: "answer", DefinitionID: field.ID}}}
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	require.NoError(t, f.a.CreateInboxIntakeMapping(r))
	require.Equal(t, 422, r.RequestCtx.Response.StatusCode())
	person := testutil.CreateTestUser(t, f.db, f.org.ID)
	request := testutil.NewGETRequest(t)
	testutil.SetFullAuthContext(request, f.org.ID, person.ID, nil, true)
	require.NoError(t, f.a.ListInboxIntakeMappings(request))
	require.Equal(t, 403, request.RequestCtx.Response.StatusCode(), "request super-user flags are not fresh database authority")
	var count int64
	require.NoError(t, f.db.Model(&models.InboxIntakeMapping{}).Where("organization_id=? AND name=?", f.org.ID, input.Name).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntakeSummaryScopesCaseAccountCurrentAssignmentAndContactFields(t *testing.T) {
	f := newIntakeBindingFixture(t)
	field := intakeField(t, f.responsePolicyFixture, "hidden_contact", "contact", "text", nil)
	f.mapping.Fields = append(f.mapping.Fields, models.InboxIntakeInput{Input: "hidden_contact", DefinitionID: field.ID})
	require.NoError(t, f.db.Save(&f.mapping).Error)
	binding := f.bind(t)
	response := f.response(binding)
	response.Inputs["hidden_contact"] = "Synthetic confidential intake value"
	_, err := f.a.receiveInboxIntake(response, binding.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	c := f.apply(t, binding, models.InboxCase{})
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "intake-case-reader", []string{"chat:read"})
	agent := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.db.Model(&c).Update("assignee_id", agent.ID).Error)
	read := func(account, caseID uuid.UUID) (int, string) {
		r := testutil.NewGETRequest(t)
		testutil.SetFullAuthContext(r, f.org.ID, agent.ID, &role.ID, false)
		testutil.SetPathParam(r, "account_id", account.String())
		testutil.SetPathParam(r, "id", f.contact.ID.String())
		r.RequestCtx.QueryArgs().Set("case_id", caseID.String())
		require.NoError(t, f.a.GetInboxIntakeSummary(r))
		return r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body())
	}
	status, body := read(f.account.ID, c.ID)
	require.Equal(t, 200, status)
	require.Contains(t, body, `"contact_fields_hidden":true`)
	require.Contains(t, body, f.field.Label)
	require.NotContains(t, body, field.Label)
	require.NotContains(t, body, "Synthetic confidential intake value")
	require.NotContains(t, body, "encrypted_answers")
	other := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	status, _ = read(other.ID, c.ID)
	require.Equal(t, 404, status)
	newCase := newInboxCase(inboxThread{f.org.ID, f.account.ID, f.contact.ID}, c.OpenedAt.Add(time.Minute), &f.admin.ID, nil)
	require.NoError(t, f.db.Create(&newCase).Error)
	status, _ = read(f.account.ID, c.ID)
	require.Equal(t, 404, status, "historical assignment cannot restore access after a case moves")
}

func TestInboxIntakeGraphValidationPreservesLegacyAndRejectsUnsafeMappingComposition(t *testing.T) {
	f := newIntakeBindingFixture(t)
	require.NoError(t, validateChatIntakeGraph(f.db, f.org.ID, f.admin.ID, "sms", nil))
	require.NoError(t, validateChatIntakeGraph(f.db, f.org.ID, f.admin.ID, "whatsapp", models.JSONB{"version": 1, "legacy": "unchanged"}))
	require.NoError(t, validateChatIntakeGraph(f.db, f.org.ID, f.admin.ID, "whatsapp", f.flow.Graph))
	require.ErrorContains(t, validateChatIntakeGraph(f.db, f.org.ID, f.admin.ID, "sms", f.flow.Graph), "whatsapp_prompt_required")
	graph, err := parseChatGraph(f.flow.Graph)
	require.NoError(t, err)
	node := graph.Node(f.node.ID)
	node.Type = ChatNodePrompt
	// The reusable form mapping has an input other than the prompt's answer.
	var encoded models.JSONB
	marshalIntakeGraph := func() {
		raw, err := json.Marshal(graph)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &encoded))
	}
	marshalIntakeGraph()
	require.ErrorContains(t, validateChatIntakeGraph(f.db, f.org.ID, f.admin.ID, "whatsapp", encoded), "prompt_answer_required")
	node.Type = ChatNodeWhatsAppFlow
	node.Config["delivery_options"] = map[string]any{"fallback_enabled": true}
	marshalIntakeGraph()
	require.ErrorContains(t, validateChatIntakeGraph(f.db, f.org.ID, f.admin.ID, "whatsapp", encoded), "whatsapp_prompt_required")
}
