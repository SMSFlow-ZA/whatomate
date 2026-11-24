package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func sequenceFixture(t *testing.T, actions func(integrationFixture) []models.InboxAction) integrationFixture {
	t.Helper()
	f := newIntegrationFixture(t)
	f.w.Actions = actions(f)
	require.NoError(t, f.a.DB.Save(&f.w).Error)
	require.NoError(t, f.a.DB.First(&f.w, "id=?", f.w.ID).Error)
	t.Cleanup(func() {
		f.a.DB.Model(&models.InboxWorkflowRun{}).Where("organization_id=? AND sequenced", f.c.OrganizationID).Update("state", inboxSequenceCancelled)
	})
	return f
}

func queueSequenceMacro(t *testing.T, f integrationFixture) models.InboxWorkflowRun {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]any{"case_id": f.c.ID})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("id", f.w.ID.String())
	require.NoError(t, f.a.PreviewInboxWorkflow(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var preview struct {
		Confirmation string                         `json:"confirmation"`
		Eligible     bool                           `json:"actions_eligible"`
		Integrations map[int]inboxIntegrationIntent `json:"integrations"`
	}
	testutil.ParseEnvelopeResponse(t, r, &preview)
	require.True(t, preview.Eligible, string(testutil.GetResponseBody(r)))
	require.NotEmpty(t, preview.Integrations)
	r = testutil.NewJSONRequest(t, map[string]any{"case_id": f.c.ID, "run_id": uuid.New(), "confirmation": preview.Confirmation})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("id", f.w.ID.String())
	require.NoError(t, f.a.ExecuteInboxMacro(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	var result struct {
		Run models.InboxWorkflowRun `json:"run"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	require.True(t, result.Run.Sequenced)
	require.Equal(t, inboxSequenceRunning, result.Run.State)
	return result.Run
}

func driveSequence(t *testing.T, f integrationFixture, runID uuid.UUID, steps int) models.InboxWorkflowRun {
	t.Helper()
	for i := 0; i < steps; i++ {
		require.NoError(t, f.a.runInboxSequences(context.Background()))
	}
	var run models.InboxWorkflowRun
	require.NoError(t, f.a.DB.First(&run, "id=?", runID).Error)
	return run
}

func TestInboxSequenceMacroPreservesOrderAndNeverRepeatsLocalActions(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "note", Value: "Before integration"}, {Type: "priority", Value: "high"}, {Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "note", Value: "After integration"}, {Type: "status", Value: "pending"}}
	})
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		var before, after int64
		require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "Before integration").Count(&before).Error)
		require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "After integration").Count(&after).Error)
		require.EqualValues(t, 1, before)
		require.Zero(t, after)
		var c models.InboxCase
		require.NoError(t, f.a.DB.First(&c, "id=?", f.c.ID).Error)
		require.Equal(t, models.InboxCasePriority("high"), c.Priority)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-ORDER"`)}}, nil
	})
	run := queueSequenceMacro(t, f)
	require.Zero(t, calls.Load())
	final := driveSequence(t, f, run.ID, 12)
	require.Equal(t, inboxRunApplied, final.State)
	require.Equal(t, len(f.w.Actions), final.NextAction)
	require.EqualValues(t, 1, calls.Load())
	for _, outcome := range final.ActionOutcomes {
		require.Contains(t, []string{"applied", "succeeded"}, outcome.Status)
	}
	_ = driveSequence(t, f, run.ID, 4)
	require.EqualValues(t, 1, calls.Load())
	var notes int64
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND kind='workflow_note'", f.c.ID).Count(&notes).Error)
	require.EqualValues(t, 2, notes)
}

func TestInboxSequenceConcurrentWorkersAndUnknownStopDownstream(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "note", Value: "Before uncertain integration"}, {Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "note", Value: "Must wait for reconciliation"}}
	})
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{}, errors.New("synthetic lost response")
	})
	run := queueSequenceMacro(t, f)
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- f.a.runInboxSequences(context.Background()) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	final := driveSequence(t, f, run.ID, 5)
	require.Equal(t, inboxSequenceUnknown, final.State)
	require.Equal(t, 1, final.NextAction)
	require.EqualValues(t, 1, calls.Load())
	var notes int64
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "Must wait for reconciliation").Count(&notes).Error)
	require.Zero(t, notes)
	// The durable cursor and terminal receipt survive a new application worker.
	restarted := *f.a.Config
	f.a = &App{DB: f.a.DB, Config: &restarted, Log: f.a.Log, InboxIntegrationExecutor: f.a.InboxIntegrationExecutor}
	final = driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxSequenceUnknown, final.State)
	require.EqualValues(t, 1, calls.Load())
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("run_id", run.ID.String())
	require.NoError(t, f.a.PreviewInboxSequence(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r), "unknown submissions require reconciliation, not context review")
}

func TestInboxSequenceRulesWaitAndResumeInRuleOrder(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "priority", Value: "high"}}
	})
	now := time.Now().UTC()
	enabled := now.Add(-time.Second)
	f.w.Kind, f.w.Trigger, f.w.EnabledAt = "rule", "incoming_message", &enabled
	require.NoError(t, f.a.DB.Save(&f.w).Error)
	later := models.InboxWorkflow{OrganizationID: f.c.OrganizationID, CreatedByID: f.actor.ID, Name: "Later rule", Kind: "rule", Trigger: "incoming_message", EnabledAt: &enabled, Position: 10, Conditions: models.InboxQueueFilter{Priority: "high"}, Actions: []models.InboxAction{{Type: "note", Value: "Later rule after integration"}}}
	require.NoError(t, f.a.DB.Create(&later).Error)
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-RULE"`)}}, nil
	})
	event := models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.c.OrganizationID, AccountID: f.c.AccountID, CaseID: f.c.ID, Kind: "incoming_message", WorkflowTrigger: "incoming_message"}
	require.NoError(t, f.a.DB.Create(&event).Error)
	require.NoError(t, f.a.processInboxRuleEvent(context.Background(), event))
	var stored models.InboxCaseActivity
	require.NoError(t, f.a.DB.First(&stored, "id=?", event.ID).Error)
	require.Nil(t, stored.WorkflowProcessedAt)
	for i := 0; i < 10; i++ {
		require.NoError(t, f.a.runInboxWorkflowCycle(context.Background()))
	}
	require.NoError(t, f.a.DB.First(&stored, "id=?", event.ID).Error)
	require.NotNil(t, stored.WorkflowProcessedAt)
	require.EqualValues(t, 1, calls.Load())
	var notes int64
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "Later rule after integration").Count(&notes).Error)
	require.EqualValues(t, 1, notes)
	require.NoError(t, f.a.processInboxRuleEvent(context.Background(), event))
	require.EqualValues(t, 1, calls.Load())
}

func TestInboxSequenceIntegrationCompletesRequiredFieldsBeforeResolve(t *testing.T) {
	f := newIntegrationFixture(t)
	field := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_reference", Label: "External reference", Kind: "text", RequiredOnResolve: true}
	require.NoError(t, f.a.DB.Create(&field).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: field.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	f.w.Actions = []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "status", Value: "resolved"}}
	require.NoError(t, f.a.DB.Save(&f.w).Error)
	require.NoError(t, f.a.DB.First(&f.w, "id=?", f.w.ID).Error)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-REQUIRED"`)}}, nil
	})
	run := queueSequenceMacro(t, f)
	final := driveSequence(t, f, run.ID, 7)
	require.Equal(t, inboxRunApplied, final.State)
	var c models.InboxCase
	require.NoError(t, f.a.DB.First(&c, "id=?", f.c.ID).Error)
	require.Equal(t, models.InboxCaseResolved, c.Status)
}

func TestInboxSequencePreviewBindsIntegrationRevisionAndKeepsPathsPrivate(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	})
	r := testutil.NewJSONRequest(t, map[string]any{"case_id": f.c.ID})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("id", f.w.ID.String())
	require.NoError(t, f.a.PreviewInboxWorkflow(r))
	body := string(testutil.GetResponseBody(r))
	require.Contains(t, body, "https://crm.example.invalid")
	require.NotContains(t, body, "/cases")
	require.NotContains(t, body, "synthetic-secret")
	require.NotContains(t, body, "enc:")
	var preview struct {
		Confirmation string `json:"confirmation"`
	}
	testutil.ParseEnvelopeResponse(t, r, &preview)
	require.NoError(t, f.a.DB.Model(&f.d).Update("description", "Changed after review").Error)
	r = testutil.NewJSONRequest(t, map[string]any{"case_id": f.c.ID, "run_id": uuid.New(), "confirmation": preview.Confirmation})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("id", f.w.ID.String())
	require.NoError(t, f.a.ExecuteInboxMacro(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
}

func prepareSequenceSender(t *testing.T, f integrationFixture, beforeResponse func(), calls *atomic.Int32) {
	t.Helper()
	// The generic contact fixture has a random hexadecimal suffix. A sending
	// fixture needs a valid synthetic E.164 destination, even with fake transport.
	require.NoError(t, f.a.DB.Model(&models.Contact{}).Where("id=?", f.c.ContactID).Update("phone_number", "+12025550123").Error)
	f.a.Redis = testutil.SetupTestRedis(t)
	require.NotNil(t, f.a.Redis)
	f.a.Config.InboxAutomation.DispatchEnabled = true
	f.a.WhatsApp = whatsapp.NewWithTimeout(f.a.Log, time.Second)
	f.a.WhatsApp.HTTPClient = &http.Client{Transport: inboxFixtureTransport(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if beforeResponse != nil {
			beforeResponse()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"messages":[{"id":"wamid.sequence.synthetic"}]}`))}, nil
	})}
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-SEND"`)}}, nil
	})
	var account models.WhatsAppAccount
	require.NoError(t, f.a.DB.First(&account, "id=?", f.c.AccountID).Error)
	require.NoError(t, f.a.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: time.Now().UTC().Add(-time.Minute)}, OrganizationID: f.c.OrganizationID, ContactID: f.c.ContactID, WhatsAppAccount: account.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText}).Error)
}

func TestInboxSequenceSendsOnceAndRecordsOnlyItsOwnCaseChange(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "send_text", Value: "Synthetic sequence reply"}, {Type: "note", Value: "After successful reply"}}
	})
	var calls atomic.Int32
	prepareSequenceSender(t, f, nil, &calls)
	run := queueSequenceMacro(t, f)
	final := driveSequence(t, f, run.ID, 10)
	require.Equal(t, inboxRunSent, final.State)
	require.Equal(t, 3, final.NextAction)
	require.EqualValues(t, 1, calls.Load())
	var count int64
	require.NoError(t, f.a.DB.Model(&models.Message{}).Where("id=? AND direction='outgoing'", run.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "After successful reply").Count(&count).Error)
	require.EqualValues(t, 1, count)
	_ = driveSequence(t, f, run.ID, 5)
	require.EqualValues(t, 1, calls.Load())
}

func sequenceReviewRequest(t *testing.T, f integrationFixture, actor, id uuid.UUID) string {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, actor)
	r.RequestCtx.SetUserValue("run_id", id.String())
	require.NoError(t, f.a.PreviewInboxSequence(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	var result struct {
		Confirmation string `json:"confirmation"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	return result.Confirmation
}

func resumeSequenceRequest(t *testing.T, f integrationFixture, actor, id uuid.UUID, confirmation string) int {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]any{"confirmation": confirmation, "reason": "Reviewed synthetic case changes"})
	testutil.SetAuthContext(r, f.c.OrganizationID, actor)
	r.RequestCtx.SetUserValue("run_id", id.String())
	require.NoError(t, f.a.ResumeInboxSequence(r))
	return testutil.GetResponseStatusCode(r)
}

func TestInboxSequenceConcurrentEditDuringSendRequiresReviewWithoutRepeatingSend(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "send_text", Value: "Synthetic reply before review"}, {Type: "note", Value: "Only after context review"}}
	})
	var calls atomic.Int32
	prepareSequenceSender(t, f, func() {
		require.NoError(t, f.a.DB.Model(&models.InboxCase{}).Where("id=?", f.c.ID).Updates(map[string]any{"priority": "high", "version": gorm.Expr("version+1")}).Error)
	}, &calls)
	run := queueSequenceMacro(t, f)
	paused := driveSequence(t, f, run.ID, 8)
	require.Equal(t, inboxSequenceReview, paused.State)
	require.Equal(t, 2, paused.NextAction)
	require.EqualValues(t, 1, calls.Load())
	confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	require.NoError(t, f.a.DB.Model(&models.InboxCase{}).Where("id=?", f.c.ID).Updates(map[string]any{"priority": "low", "version": gorm.Expr("version+1")}).Error)
	require.Equal(t, 409, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
	confirmation = sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	require.Equal(t, 200, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
	final := driveSequence(t, f, run.ID, 4)
	require.Equal(t, inboxRunSent, final.State)
	require.EqualValues(t, 1, calls.Load())
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "Only after context review").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxSequenceSupervisorCanResumeWithOwnPermissionsAndKeepsInitiator(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "note", Value: "Supervisor completed the sequence"}}
	})
	supervisor := f.actor
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Assigned integration operator", []string{"chat:read", "chat:write", "custom_actions:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	f.actor = agent
	require.NoError(t, f.a.DB.Model(&models.InboxCase{}).Where("id=?", f.c.ID).Update("assignee_id", agent.ID).Error)
	require.NoError(t, f.a.DB.First(&f.c, "id=?", f.c.ID).Error)
	run := queueSequenceMacro(t, f)
	require.NoError(t, f.a.DB.Model(&models.InboxCase{}).Where("id=?", f.c.ID).Update("assignee_id", supervisor.ID).Error)
	paused := driveSequence(t, f, run.ID, 1)
	require.Equal(t, inboxSequenceBlocked, paused.State)
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, agent.ID)
	r.RequestCtx.SetUserValue("run_id", run.ID.String())
	require.NoError(t, f.a.PreviewInboxSequence(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	r = testutil.NewJSONRequest(t, map[string]any{"case_id": f.c.ID, "run_id": run.ID, "confirmation": strings.Repeat("f", 64)})
	testutil.SetAuthContext(r, f.c.OrganizationID, agent.ID)
	r.RequestCtx.SetUserValue("id", f.w.ID.String())
	require.NoError(t, f.a.ExecuteInboxMacro(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r), "replaying a run must recheck current case access")
	confirmation := sequenceReviewRequest(t, f, supervisor.ID, run.ID)
	require.Equal(t, 200, resumeSequenceRequest(t, f, supervisor.ID, run.ID, confirmation))
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-SUPERVISOR"`)}}, nil
	})
	final := driveSequence(t, f, run.ID, 7)
	require.Equal(t, inboxRunApplied, final.State)
	require.Equal(t, supervisor.ID, final.ActorID)
	require.NotNil(t, final.InitiatedByID)
	require.Equal(t, agent.ID, *final.InitiatedByID)
}

func TestInboxSequenceReservesOnlyOneAutomaticReplyPerEvent(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "send_text", Value: "First synthetic automatic reply"}}
	})
	var calls atomic.Int32
	prepareSequenceSender(t, f, nil, &calls)
	require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("inbox_automation_sends_enabled", true).Error)
	enabled := time.Now().UTC().Add(-time.Minute)
	f.w.Kind, f.w.Trigger, f.w.EnabledAt = "rule", "incoming_message", &enabled
	require.NoError(t, f.a.DB.Save(&f.w).Error)
	later := models.InboxWorkflow{OrganizationID: f.c.OrganizationID, CreatedByID: f.actor.ID, Name: "Later automatic reply", Kind: "rule", Trigger: "incoming_message", EnabledAt: &enabled, Position: 10, Actions: []models.InboxAction{{Type: "send_text", Value: "Must not be sent"}, {Type: "note", Value: "Later local work still applies"}}}
	require.NoError(t, f.a.DB.Create(&later).Error)
	event := models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.c.OrganizationID, AccountID: f.c.AccountID, CaseID: f.c.ID, Kind: "incoming_message", WorkflowTrigger: "incoming_message"}
	require.NoError(t, f.a.DB.Create(&event).Error)
	for i := 0; i < 12; i++ {
		require.NoError(t, f.a.runInboxWorkflowCycle(context.Background()))
	}
	require.EqualValues(t, 1, calls.Load())
	var run models.InboxWorkflowRun
	require.NoError(t, f.a.DB.First(&run, "event_id=? AND workflow_id=?", event.ID, later.ID).Error)
	require.Equal(t, "event_send_already_selected", run.Outcome)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "Later local work still applies").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxSequenceParkedEventsDoNotStarveUnrelatedCases(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "note", Value: "Unrelated case processed"}}
	})
	enabled := time.Now().UTC().Add(-time.Hour)
	f.w.Kind, f.w.Trigger, f.w.EnabledAt = "rule", "incoming_message", &enabled
	require.NoError(t, f.a.DB.Save(&f.w).Error)
	for i := 0; i < 52; i++ {
		contact := testutil.CreateTestContact(t, f.a.DB, f.c.OrganizationID)
		c := newInboxCase(inboxThread{f.c.OrganizationID, f.c.AccountID, contact.ID}, time.Now().UTC(), nil, nil)
		require.NoError(t, f.a.DB.Create(&c).Error)
		event := models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: time.Now().UTC().Add(-time.Minute)}, OrganizationID: c.OrganizationID, AccountID: c.AccountID, CaseID: c.ID, Kind: "incoming_message", WorkflowTrigger: "incoming_message"}
		require.NoError(t, f.a.DB.Create(&event).Error)
		run := newInboxWorkflowRun(uuid.New(), f.w, c, f.actor.ID)
		run.EventID, run.Sequenced, run.State = &event.ID, true, inboxSequenceReview
		require.NoError(t, f.a.DB.Create(&run).Error)
	}
	event := models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.c.OrganizationID, AccountID: f.c.AccountID, CaseID: f.c.ID, Kind: "incoming_message", WorkflowTrigger: "incoming_message"}
	require.NoError(t, f.a.DB.Create(&event).Error)
	require.NoError(t, f.a.runInboxWorkflowCycle(context.Background()))
	require.NoError(t, f.a.DB.First(&event, "id=?", event.ID).Error)
	require.NotNil(t, event.WorkflowProcessedAt)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "Unrelated case processed").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxSequenceHeldRepliesDoNotStarveIntegrationWork(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "send_text", Value: "Held synthetic reply"}, {Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	})
	var sends, integrations atomic.Int32
	prepareSequenceSender(t, f, nil, &sends)
	for i := 0; i < 25; i++ {
		queueSequenceMacro(t, f)
	}
	f.a.Config.InboxAutomation.DispatchEnabled = false
	other := f.w
	other.ID, other.Name = uuid.New(), "Runnable integration"
	other.Actions = []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	require.NoError(t, f.a.DB.Create(&other).Error)
	f.w = other
	run := queueSequenceMacro(t, f)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		integrations.Add(1)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-NOT-STARVED"`)}}, nil
	})
	require.NoError(t, f.a.runInboxSequences(context.Background()))
	require.EqualValues(t, 1, integrations.Load())
	require.Zero(t, sends.Load())
	final := driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxRunApplied, final.State)
}

func TestInboxSequenceSendReceiptReconciliationSkipsUnknownIntegrations(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	})
	for i := 0; i < 25; i++ {
		run := queueSequenceMacro(t, f)
		require.NoError(t, f.a.DB.Model(&run).Updates(map[string]any{"state": inboxSequenceUnknown, "updated_at": time.Now().UTC().Add(-time.Hour)}).Error)
	}
	other := f.w
	other.ID, other.Name = uuid.New(), "Interrupted synthetic send"
	other.Actions = []models.InboxAction{{Type: "send_text", Value: "Previously submitted"}, {Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	require.NoError(t, f.a.DB.Create(&other).Error)
	f.w = other
	var calls atomic.Int32
	prepareSequenceSender(t, f, nil, &calls)
	run := queueSequenceMacro(t, f)
	require.NoError(t, f.a.DB.Model(&run).Update("state", inboxSequenceUnknown).Error)
	require.NoError(t, f.a.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: run.ID}, OrganizationID: f.c.OrganizationID, ContactID: f.c.ContactID, Direction: models.DirectionOutgoing, WhatsAppMessageID: "wamid.already.accepted.synthetic", Status: models.MessageStatusSent}).Error)
	// Reconciliation is allowed while new dispatch is held.
	f.a.Config.InboxAutomation.IntegrationDispatchEnabled = false
	require.NoError(t, f.a.runInboxSequences(context.Background()))
	require.NoError(t, f.a.DB.First(&run, "id=?", run.ID).Error)
	require.Equal(t, 1, run.NextAction)
	require.Equal(t, inboxSequenceRunning, run.State)
	require.Zero(t, calls.Load())
}

func TestInboxSequenceChangedDefinitionNeedsNewReviewBeforeSubmission(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	})
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-REVIEWED"`)}}, nil
	})
	run := queueSequenceMacro(t, f)
	require.NoError(t, f.a.DB.Model(&f.d).Update("description", "Revised approved definition").Error)
	paused := driveSequence(t, f, run.ID, 2)
	require.Equal(t, inboxSequenceReview, paused.State)
	require.Equal(t, "integration_review_required", paused.Outcome)
	require.Zero(t, calls.Load())
	confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	require.Equal(t, 200, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
	final := driveSequence(t, f, run.ID, 4)
	require.Equal(t, inboxRunApplied, final.State)
	require.EqualValues(t, 1, calls.Load())
}

func TestInboxSequenceReviewConflictsInsteadOfWaitingOnExecutorLock(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	})
	run := queueSequenceMacro(t, f)
	dispatch, err := f.a.advanceInboxSequence(context.Background(), run.ID, f.c.OrganizationID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, dispatch.IntegrationID)
	require.NoError(t, f.a.DB.Model(&run).Update("state", inboxSequenceReview).Error)
	confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	locked := f.a.DB.Begin()
	require.NoError(t, locked.Error)
	defer locked.Rollback()
	var execution models.InboxIntegrationExecution
	require.NoError(t, locked.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id=?", dispatch.IntegrationID).Error)
	started := time.Now()
	require.Equal(t, 409, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
	require.Less(t, time.Since(started), 2*time.Second)
	require.NoError(t, f.a.DB.First(&run, "id=?", run.ID).Error)
	require.Equal(t, inboxSequenceReview, run.State)
	require.Zero(t, execution.AttemptCount)
}
