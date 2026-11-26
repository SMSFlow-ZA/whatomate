package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type integrationFixture struct {
	a        *App
	c        models.InboxCase
	actor    *models.User
	d        models.InboxIntegration
	w        models.InboxWorkflow
	run      models.InboxWorkflowRun
	mappings map[string]string
}

func newIntegrationFixture(t *testing.T) integrationFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	settings := &config.Config{}
	settings.App.EncryptionKey = strings.Repeat("synthetic-key-", 4)
	settings.InboxAutomation.IntegrationDispatchEnabled = true
	settings.InboxAutomation.IntegrationEndpoints = `[{"key":"crm","name":"Synthetic CRM","url":"https://crm.example.invalid/cases","method":"POST","idempotency_header":"Idempotency-Key"}]`
	a := &App{DB: db, Config: settings, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	actor := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Updates(map[string]any{"inbox_workflows_enabled": true, "settings": models.JSONB{"inbox_integrations_enabled": true, "synthetic_preserved": "keep"}}).Error)
	require.NoError(t, db.Model(contact).Update("profile_name", "Synthetic customer").Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	credential, err := crypto.Encrypt("synthetic-secret", settings.App.EncryptionKey)
	require.NoError(t, err)
	d := models.InboxIntegration{OrganizationID: org.ID, CreatedByID: actor.ID, Name: "Synthetic CRM case", EndpointKey: "crm", AuthMode: "bearer", Credential: credential, Enabled: true, Inputs: []models.InboxIntegrationInput{{Name: "customer", Kind: "text", Required: true}, {Name: "count", Kind: "number", Required: true}, {Name: "subscribed", Kind: "boolean", Required: true}}, Outputs: []models.InboxIntegrationOutput{}, ReferenceKey: "reference"}
	require.NoError(t, db.Create(&d).Error)
	mappings := map[string]string{"customer": "{{contact_name}}", "count": "0", "subscribed": "false"}
	w := models.InboxWorkflow{OrganizationID: org.ID, CreatedByID: actor.ID, Name: "Synthetic integration workflow", Kind: "macro", Visibility: "organization", Actions: []models.InboxAction{{Type: "integration", Value: d.ID.String(), IntegrationInputs: mappings}}}
	require.NoError(t, db.Create(&w).Error)
	run := newInboxWorkflowRun(uuid.New(), w, c, actor.ID)
	run.CaseDigest, err = inboxSendCaseDigest(db, c)
	require.NoError(t, err)
	require.NoError(t, saveInboxWorkflowRun(db, c, &run))
	return integrationFixture{a, c, actor, d, w, run, mappings}
}

func (f integrationFixture) enqueue(t *testing.T) models.InboxIntegrationExecution {
	t.Helper()
	prepared, err := f.a.prepareInboxIntegration(f.a.DB, f.c, f.actor.ID, f.d.ID, f.mappings)
	require.NoError(t, err)
	require.True(t, prepared.Intent.Available)
	execution, err := f.a.enqueueInboxIntegration(f.a.DB, f.run, 0, prepared, f.mappings)
	require.NoError(t, err)
	return execution
}

type fakeIntegration func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error)

func (f fakeIntegration) Execute(ctx context.Context, p inboxintegration.Endpoint, mode, credential string, body []byte, id string) (inboxintegration.Response, error) {
	return f(ctx, p, mode, credential, body, id)
}

func TestInboxIntegrationCRUDSecretsAndEmergencySettings(t *testing.T) {
	f := newIntegrationFixture(t)
	input := map[string]any{"name": "Second synthetic integration", "endpoint_key": "crm", "auth_mode": "bearer", "credential": "second-synthetic-secret", "enabled": true, "inputs": f.d.Inputs, "outputs": f.d.Outputs, "reference_key": "reference"}
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	require.NoError(t, f.a.CreateInboxIntegration(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NotContains(t, string(testutil.GetResponseBody(r)), "second-synthetic-secret")
	require.NotContains(t, string(testutil.GetResponseBody(r)), "enc:")
	var result struct {
		Integration struct {
			ID            uuid.UUID `json:"id"`
			Enabled       bool      `json:"enabled"`
			HasCredential bool      `json:"has_credential"`
		} `json:"integration"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	require.False(t, result.Integration.Enabled)
	require.True(t, result.Integration.HasCredential)
	var stored models.InboxIntegration
	require.NoError(t, f.a.DB.First(&stored, "id=?", result.Integration.ID).Error)
	require.True(t, crypto.IsEncrypted(stored.Credential))
	previous := stored.Credential
	delete(input, "credential")
	input["updated_at"] = stored.UpdatedAt
	r = testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("integration_id", stored.ID.String())
	require.NoError(t, f.a.UpdateInboxIntegration(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NoError(t, f.a.DB.First(&stored, "id=?", stored.ID).Error)
	require.Equal(t, previous, stored.Credential)
	require.True(t, stored.Enabled)
	// Changing this switch preserves unrelated organization settings.
	r = testutil.NewJSONRequest(t, map[string]any{"enabled": false})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	require.NoError(t, f.a.UpdateInboxIntegrationSettings(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var org models.Organization
	require.NoError(t, f.a.DB.First(&org, "id=?", f.c.OrganizationID).Error)
	require.Equal(t, "keep", org.Settings["synthetic_preserved"])
	prepared, err := f.a.prepareInboxIntegration(f.a.DB, f.c, f.actor.ID, f.d.ID, f.mappings)
	require.NoError(t, err)
	require.False(t, prepared.Intent.Available)
	require.Equal(t, "dispatch_disabled", prepared.Intent.Reason)
	foreign := testutil.CreateTestOrganization(t, f.a.DB)
	foreignActor := testutil.CreateTestUser(t, f.a.DB, foreign.ID, testutil.WithSuperAdmin())
	r = testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, foreign.ID, foreignActor.ID)
	r.RequestCtx.SetUserValue("integration_id", stored.ID.String())
	require.NoError(t, f.a.UpdateInboxIntegration(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	// Missing instance encryption cannot turn credential writes into plaintext.
	f.a.Config.App.EncryptionKey = ""
	r = testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	require.NoError(t, f.a.CreateInboxIntegration(r))
	require.Equal(t, 503, testutil.GetResponseStatusCode(r))
}

func TestInboxIntegrationPreparationTypesScopeAndPrivacy(t *testing.T) {
	f := newIntegrationFixture(t)
	prepared, err := f.a.prepareInboxIntegration(f.a.DB, f.c, f.actor.ID, f.d.ID, f.mappings)
	require.NoError(t, err)
	require.JSONEq(t, `{"customer":"Synthetic customer","count":0,"subscribed":false}`, string(prepared.Body))
	require.Equal(t, "https://crm.example.invalid", prepared.Intent.Destination)
	for _, mappings := range []map[string]string{
		{"customer": "{{unknown_secret}}", "count": "0", "subscribed": "false"},
		{"customer": "Example", "count": "null", "subscribed": "false"},
		{"customer": "Example", "count": "0", "subscribed": "no"},
		{"customer": "Example", "count": "0", "subscribed": "false", "extra": "not declared"},
	} {
		_, err := f.a.prepareInboxIntegration(f.a.DB, f.c, f.actor.ID, f.d.ID, mappings)
		require.Error(t, err)
	}
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Scoped integration agent", []string{"chat:read", "chat:write", "custom_actions:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", agent.ID).Error)
	_, err = f.a.prepareInboxIntegration(f.a.DB, f.c, agent.ID, f.d.ID, f.mappings)
	require.NoError(t, err)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.a.DB, f.c.OrganizationID)
	other := newInboxCase(inboxThread{f.c.OrganizationID, otherAccount.ID, f.c.ContactID}, time.Now().UTC(), nil, nil)
	require.NoError(t, f.a.DB.Create(&other).Error)
	_, err = f.a.prepareInboxIntegration(f.a.DB, other, agent.ID, f.d.ID, f.mappings)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("settings", models.JSONB{"mask_phone_numbers": true, "inbox_integrations_enabled": true}).Error)
	masked := map[string]string{"customer": "{{phone_number}}", "count": "0", "subscribed": "false"}
	_, err = f.a.prepareInboxIntegration(f.a.DB, f.c, f.actor.ID, f.d.ID, masked)
	require.Equal(t, "inputs_missing", integrationProblemReason(err))
}

func TestInboxIntegrationDurableClaimConcurrentReplayAndRestart(t *testing.T) {
	f := newIntegrationFixture(t)
	execution := f.enqueue(t)
	require.Equal(t, execution.ID, f.enqueue(t).ID)
	require.True(t, crypto.IsEncrypted(execution.EncryptedRequest))
	require.NotContains(t, execution.EncryptedRequest, "Synthetic customer")
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	f.a.InboxIntegrationExecutor = fakeIntegration(func(_ context.Context, p inboxintegration.Endpoint, mode, credential string, body []byte, id string) (inboxintegration.Response, error) {
		calls.Add(1)
		require.Equal(t, execution.ID.String(), id)
		require.Equal(t, "synthetic-secret", credential)
		require.Equal(t, "bearer", mode)
		require.Equal(t, "POST", p.Method)
		require.JSONEq(t, `{"customer":"Synthetic customer","count":0,"subscribed":false}`, string(body))
		close(started)
		<-release
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"SYNTHETIC-1"`)}, Duration: time.Millisecond}, nil
	})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.a.dispatchInboxIntegration(context.Background(), execution.ID) }()
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fake provider not reached")
	}
	// A case/contact lock remains available while the external request waits.
	lockErr := f.a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout='300ms'").Error; err != nil {
			return err
		}
		return (inboxThread{f.c.OrganizationID, f.c.AccountID, f.c.ContactID}).lock(tx)
	})
	close(release)
	wg.Wait()
	close(errs)
	require.NoError(t, lockErr)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, calls.Load())
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "succeeded", stored.State)
	require.Equal(t, 1, stored.AttemptCount)
	require.True(t, crypto.IsEncrypted(stored.EncryptedResult))
	require.NotContains(t, stored.EncryptedResult, "SYNTHETIC-1")
	// A restarted worker sees the durable terminal state, not a new operation.
	restarted := &App{DB: f.a.DB, Config: f.a.Config, Log: f.a.Log, InboxIntegrationExecutor: f.a.InboxIntegrationExecutor}
	require.NoError(t, restarted.dispatchInboxIntegration(context.Background(), execution.ID))
	require.EqualValues(t, 1, calls.Load())
	raw, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "synthetic-secret")
	require.NotContains(t, string(raw), "enc:")
}

func TestInboxIntegrationUnknownAndInterruptedSubmissionsNeverReplay(t *testing.T) {
	f := newIntegrationFixture(t)
	execution := f.enqueue(t)
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{}, errors.New("lost response: synthetic-secret")
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	require.EqualValues(t, 1, calls.Load())
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "unknown", stored.State)
	require.Equal(t, "needs_reconciliation", stored.Outcome)
	f.run = newInboxWorkflowRun(uuid.New(), f.w, f.c, f.actor.ID)
	var err error
	f.run.CaseDigest, err = inboxSendCaseDigest(f.a.DB, f.c)
	require.NoError(t, err)
	require.NoError(t, saveInboxWorkflowRun(f.a.DB, f.c, &f.run))
	interrupted := f.enqueue(t)
	_, _, claimed, err := f.a.claimInboxIntegration(context.Background(), interrupted.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, f.a.reconcileInterruptedInboxIntegrations(context.Background(), time.Now().UTC().Add(3*time.Minute)))
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), interrupted.ID))
	require.EqualValues(t, 1, calls.Load())
	stored = models.InboxIntegrationExecution{}
	require.NoError(t, f.a.DB.First(&stored, "id=?", interrupted.ID).Error)
	require.Equal(t, "unknown", stored.State)
}

func TestInboxIntegrationAtomicInstanceLimitAndChangedDefinition(t *testing.T) {
	f := newIntegrationFixture(t)
	first := f.enqueue(t)
	makeExecution := func() models.InboxIntegrationExecution {
		f.run = newInboxWorkflowRun(uuid.New(), f.w, f.c, f.actor.ID)
		var err error
		f.run.CaseDigest, err = inboxSendCaseDigest(f.a.DB, f.c)
		require.NoError(t, err)
		require.NoError(t, saveInboxWorkflowRun(f.a.DB, f.c, &f.run))
		return f.enqueue(t)
	}
	second, third := makeExecution(), makeExecution()
	for _, id := range []uuid.UUID{first.ID, second.ID} {
		_, _, claimed, err := f.a.claimInboxIntegration(context.Background(), id)
		require.NoError(t, err)
		require.True(t, claimed)
	}
	_, _, claimed, err := f.a.claimInboxIntegration(context.Background(), third.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, f.a.reconcileInterruptedInboxIntegrations(context.Background(), time.Now().UTC().Add(3*time.Minute)))
	require.NoError(t, f.a.DB.Model(&f.d).Update("description", "Definition changed after approval").Error)
	_, _, claimed, err = f.a.claimInboxIntegration(context.Background(), third.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", third.ID).Error)
	require.Equal(t, "blocked", stored.State)
	require.Equal(t, "definition_or_values_changed", stored.Outcome)
}

func TestInboxIntegrationEmergencyHoldAndOutputValidation(t *testing.T) {
	f := newIntegrationFixture(t)
	field := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_reference", Label: "External reference", Kind: "number"}
	require.NoError(t, f.a.DB.Create(&field).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "external_value", DefinitionID: field.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	execution := f.enqueue(t)
	f.a.Config.InboxAutomation.IntegrationDispatchEnabled = false
	_, _, claimed, err := f.a.claimInboxIntegration(context.Background(), execution.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "pending", stored.State)
	require.Zero(t, stored.AttemptCount)
	f.a.Config.InboxAutomation.IntegrationDispatchEnabled = true
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-1"`), "external_value": json.RawMessage(`"not a number"`)}}, nil
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "unknown", stored.State)
	require.Equal(t, "output_invalid", stored.Outcome)
	require.Empty(t, stored.EncryptedResult)
	require.Nil(t, stored.OutputsAppliedAt)
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	require.EqualValues(t, 1, calls.Load())
}

func TestInboxIntegrationPermissionsDeletionAndSecretCategories(t *testing.T) {
	f := newIntegrationFixture(t)
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "No integration privilege", []string{"chat:read", "chat:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	_, err := f.a.prepareInboxIntegration(f.a.DB, f.c, agent.ID, f.d.ID, f.mappings)
	require.Equal(t, "permission_required", integrationProblemReason(err))
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, f.c.OrganizationID, agent.ID)
	require.NoError(t, f.a.ListInboxIntegrations(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	r = testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	require.NoError(t, f.a.ListInboxIntegrations(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NotContains(t, string(testutil.GetResponseBody(r)), "synthetic-secret")
	require.NotContains(t, string(testutil.GetResponseBody(r)), "enc:")
	r = testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("integration_id", f.d.ID.String())
	require.NoError(t, f.a.DeleteInboxIntegration(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	execution := f.enqueue(t)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{}, inboxintegration.Failure{Category: "error includes synthetic-secret", Ambiguous: false}
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "provider_failure", stored.Outcome)
}

func TestInboxIntegrationOutputFieldsDoNotBroadenContactAccess(t *testing.T) {
	f := newIntegrationFixture(t)
	field := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "contact", Key: "external_reference", Label: "External reference", Kind: "text"}
	require.NoError(t, f.a.DB.Create(&field).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "external_reference", DefinitionID: field.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Integration without contact writes", []string{"chat:read", "chat:write", "custom_actions:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", agent.ID).Error)
	_, err := f.a.prepareInboxIntegration(f.a.DB, f.c, agent.ID, f.d.ID, f.mappings)
	require.Equal(t, "contact_field_permission_required", integrationProblemReason(err))
	foreign := testutil.CreateTestOrganization(t, f.a.DB)
	other := models.InboxFieldDefinition{OrganizationID: foreign.ID, Scope: "case", Key: "private", Label: "Private field", Kind: "text"}
	require.NoError(t, f.a.DB.Create(&other).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "external_reference", DefinitionID: other.ID}}
	require.ErrorIs(t, f.a.validateInboxIntegration(f.a.DB, f.d), gorm.ErrRecordNotFound)
}

func TestInboxIntegrationAppliesTypedOutputsOnceWithoutRuleTriggers(t *testing.T) {
	f := newIntegrationFixture(t)
	count := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_count", Label: "External count", Kind: "number"}
	flag := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "contact", Key: "external_flag", Label: "External flag", Kind: "boolean"}
	require.NoError(t, f.a.DB.Create(&count).Error)
	require.NoError(t, f.a.DB.Create(&flag).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "count", DefinitionID: count.ID}, {Name: "flag", DefinitionID: flag.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	execution := f.enqueue(t)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-1"`), "count": json.RawMessage(`0`), "flag": json.RawMessage(`false`)}}, nil
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "succeeded", stored.State)
	require.NotNil(t, stored.OutputsAppliedAt)
	var values []models.InboxFieldValue
	require.NoError(t, f.a.DB.Where("organization_id=? AND definition_id IN ?", f.c.OrganizationID, []uuid.UUID{count.ID, flag.ID}).Find(&values).Error)
	require.Len(t, values, 2)
	for _, value := range values {
		if value.DefinitionID == count.ID {
			require.Equal(t, f.c.ID, value.SubjectID)
			require.JSONEq(t, `0`, string(value.Value))
		} else {
			require.Equal(t, f.c.ContactID, value.SubjectID)
			require.JSONEq(t, `false`, string(value.Value))
		}
	}
	var current models.InboxCase
	require.NoError(t, f.a.DB.First(&current, "id=?", f.c.ID).Error)
	require.Equal(t, f.c.Version+1, current.Version)
	var activities []models.InboxCaseActivity
	require.NoError(t, f.a.DB.Where("case_id=? AND kind='workflow_integration_result'", f.c.ID).Find(&activities).Error)
	require.Len(t, activities, 1)
	require.Empty(t, activities[0].WorkflowTrigger)
}

func TestInboxIntegrationLateResultDoesNotOverwriteNewerFields(t *testing.T) {
	f := newIntegrationFixture(t)
	field := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_count", Label: "External count", Kind: "number"}
	require.NoError(t, f.a.DB.Create(&field).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "count", DefinitionID: field.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	execution := f.enqueue(t)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		require.NoError(t, f.a.DB.Create(&models.InboxFieldValue{OrganizationID: f.c.OrganizationID, DefinitionID: field.ID, SubjectID: f.c.ID, Value: json.RawMessage(`99`)}).Error)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-1"`), "count": json.RawMessage(`0`)}}, nil
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "succeeded", stored.State)
	require.Equal(t, "output_context_changed", stored.Outcome)
	require.Nil(t, stored.OutputsAppliedAt)
	require.True(t, crypto.IsEncrypted(stored.EncryptedResult))
	var value models.InboxFieldValue
	require.NoError(t, f.a.DB.First(&value, "organization_id=? AND definition_id=? AND subject_id=?", f.c.OrganizationID, field.ID, f.c.ID).Error)
	require.JSONEq(t, `99`, string(value.Value))
}

func TestInboxIntegrationInvalidOutputBatchChangesNoFields(t *testing.T) {
	f := newIntegrationFixture(t)
	first := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_count", Label: "External count", Kind: "number"}
	second := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_flag", Label: "External flag", Kind: "boolean"}
	require.NoError(t, f.a.DB.Create(&first).Error)
	require.NoError(t, f.a.DB.Create(&second).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "count", DefinitionID: first.ID}, {Name: "flag", DefinitionID: second.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	execution := f.enqueue(t)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-1"`), "count": json.RawMessage(`3`), "flag": json.RawMessage(`"not boolean"`)}}, nil
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.Zero(t, count)
	var stored models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&stored, "id=?", execution.ID).Error)
	require.Equal(t, "unknown", stored.State)
	require.Equal(t, "output_invalid", stored.Outcome)
}

func TestInboxIntegrationEndpointScopesAndSecretPathsRemainPrivate(t *testing.T) {
	f := newIntegrationFixture(t)
	foreign := testutil.CreateTestOrganization(t, f.a.DB)
	endpoints := []inboxintegration.Endpoint{
		{OrganizationID: f.c.OrganizationID.String(), Key: "crm", Name: "Own CRM", URL: "https://crm.example.invalid/private-webhook-token", Method: "POST"},
		{OrganizationID: foreign.ID.String(), Key: "other_crm", Name: "Other CRM", URL: "https://other.example.invalid/other-secret-path", Method: "POST"},
	}
	raw, err := json.Marshal(endpoints)
	require.NoError(t, err)
	f.a.Config.InboxAutomation.IntegrationEndpoints = string(raw)
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	require.NoError(t, f.a.ListInboxIntegrations(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	body := string(testutil.GetResponseBody(r))
	require.Contains(t, body, "Own CRM")
	require.NotContains(t, body, "Other CRM")
	require.NotContains(t, body, "other.example.invalid")
	require.NotContains(t, body, "private-webhook-token")
	require.NotContains(t, body, "other-secret-path")
	prepared, err := f.a.prepareInboxIntegration(f.a.DB, f.c, f.actor.ID, f.d.ID, f.mappings)
	require.NoError(t, err)
	require.Equal(t, "https://crm.example.invalid", prepared.Intent.Destination)
	f.d.EndpointKey = "other_crm"
	require.ErrorIs(t, f.a.validateInboxIntegration(f.a.DB, f.d), errInboxTransition)
}
