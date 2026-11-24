package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
)

func dependencyField(t *testing.T, f integrationFixture, key, kind string) models.InboxFieldDefinition {
	t.Helper()
	field := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: key, Label: "Synthetic " + key, Kind: kind}
	require.NoError(t, f.a.DB.Create(&field).Error)
	return field
}

func saveDependencyWorkflow(t *testing.T, f *integrationFixture, actions []models.InboxAction) {
	t.Helper()
	f.w.Actions = actions
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	require.NoError(t, f.a.DB.Save(&f.w).Error)
	require.NoError(t, f.a.DB.First(&f.w, "id=?", f.w.ID).Error)
	t.Cleanup(func() {
		f.a.DB.Model(&models.InboxWorkflowRun{}).Where("organization_id=? AND sequenced", f.c.OrganizationID).Update("state", inboxSequenceCancelled)
	})
}

func dependencyPreview(t *testing.T, f integrationFixture) inboxWorkflowPreview {
	t.Helper()
	account, contact, err := f.a.inboxWorkflowContext(f.a.DB, f.c)
	require.NoError(t, err)
	preview, err := f.a.evaluateInboxWorkflow(f.a.DB, f.w, f.c, account, contact, f.actor.ID, time.Now().UTC())
	require.NoError(t, err)
	return preview
}

func TestInboxSequenceDependenciesChainTypedInputsWithFreshReviewAndRestart(t *testing.T) {
	f := newIntegrationFixture(t)
	text := dependencyField(t, f, "ticket", "text")
	number := dependencyField(t, f, "count", "number")
	flag := dependencyField(t, f, "flag", "boolean")
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: text.ID}, {Name: "count", DefinitionID: number.ID}, {Name: "flag", DefinitionID: flag.ID}}
	second := f.d
	second.ID, second.Name, second.Outputs = uuid.New(), "Synthetic follow-up request", []models.InboxIntegrationOutput{}
	require.NoError(t, f.a.DB.Create(&second).Error)
	inputs := map[string]string{"customer": "{{case.field.ticket}}", "count": "{{case.field.count}}", "subscribed": "{{case.field.flag}}"}
	saveDependencyWorkflow(t, &f, []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "integration", Value: second.ID.String(), IntegrationInputs: inputs}, {Type: "note", Value: "After reviewed chain"}})
	preview := dependencyPreview(t, f)
	require.True(t, preview.ActionsEligible)
	require.Len(t, preview.Integrations[1].Intent.DeferredInputs, 3)
	require.Empty(t, preview.Integrations[1].Intent.Inputs)
	require.Empty(t, preview.Integrations[1].RequestDigest)
	require.Empty(t, preview.Integrations[1].Body)
	_, err := f.a.enqueueInboxIntegration(f.a.DB, f.run, 1, preview.Integrations[1], inputs)
	require.ErrorIs(t, err, errInboxTransition, "a deferred preview is not a request approval")
	var calls atomic.Int32
	var firstID, secondID string
	f.a.InboxIntegrationExecutor = fakeIntegration(func(_ context.Context, _ inboxintegration.Endpoint, _, _ string, body []byte, id string) (inboxintegration.Response, error) {
		if calls.Add(1) == 1 {
			firstID = id
			return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-CHAIN"`), "count": json.RawMessage(`0`), "flag": json.RawMessage(`false`)}}, nil
		}
		secondID = id
		require.JSONEq(t, `{"customer":"CASE-CHAIN","count":0,"subscribed":false}`, string(body))
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-SECOND"`)}}, nil
	})
	run := queueSequenceMacro(t, f)
	paused := driveSequence(t, f, run.ID, 8)
	require.Equal(t, inboxSequenceReview, paused.State)
	require.Equal(t, "prior_integration_result_requires_review", paused.Outcome)
	require.Equal(t, 1, paused.NextAction)
	require.EqualValues(t, 1, calls.Load())
	var executions int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationExecution{}).Where("run_id=?", run.ID).Count(&executions).Error)
	require.EqualValues(t, 1, executions, "no pending second request may exist before exact review")
	settings := *f.a.Config
	f.a = &App{DB: f.a.DB, Config: &settings, Log: f.a.Log, InboxIntegrationExecutor: f.a.InboxIntegrationExecutor}
	snapshot, err := f.a.reviewInboxSequence(f.a.DB, f.c.OrganizationID, f.actor.ID, run.ID)
	require.NoError(t, err)
	require.Empty(t, snapshot.Preview.DeferredActions)
	require.JSONEq(t, `{"customer":"CASE-CHAIN","count":0,"subscribed":false}`, string(snapshot.Preview.Integrations[0].Body))
	require.Equal(t, 200, resumeSequenceRequest(t, f, f.actor.ID, run.ID, snapshot.Confirmation))
	var workers sync.WaitGroup
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); failures <- f.a.runInboxSequences(context.Background()) }()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	final := driveSequence(t, f, run.ID, 8)
	require.Equal(t, inboxRunApplied, final.State)
	require.EqualValues(t, 2, calls.Load())
	require.NotEqual(t, firstID, secondID)
	_ = driveSequence(t, f, run.ID, 4)
	require.EqualValues(t, 2, calls.Load())
}

func TestInboxSequenceDependenciesReplyRequiresReviewEvenWhenReturnedValueIsUnchanged(t *testing.T) {
	f := newIntegrationFixture(t)
	field := dependencyField(t, f, "ticket", "text")
	require.NoError(t, f.a.DB.Create(&models.InboxFieldValue{OrganizationID: f.c.OrganizationID, DefinitionID: field.ID, SubjectID: f.c.ID, Value: json.RawMessage(`"CASE-SEND"`)}).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: field.ID}}
	saveDependencyWorkflow(t, &f, []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "send_text", Personalise: true, Value: `Reference {{case.field.ticket | "pending"}}`}})
	var sends atomic.Int32
	prepareSequenceSender(t, f, nil, &sends)
	preview := dependencyPreview(t, f)
	require.False(t, preview.SendUnavailable)
	require.Empty(t, preview.Digest)
	require.Equal(t, "Reference …", preview.Reply.Text)
	require.Equal(t, []string{"case.field.ticket"}, preview.Reply.DeferredVariables)
	run := queueSequenceMacro(t, f)
	paused := driveSequence(t, f, run.ID, 8)
	require.Equal(t, inboxSequenceReview, paused.State)
	require.True(t, paused.SequenceSendReserved)
	require.Zero(t, sends.Load())
	confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("definition_id=? AND subject_id=?", field.ID, f.c.ID).Update("value", json.RawMessage(`"CASE-NEWER"`)).Error)
	require.Equal(t, 409, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
	confirmation = sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	require.Equal(t, 200, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
	final := driveSequence(t, f, run.ID, 8)
	require.Equal(t, inboxRunSent, final.State)
	require.EqualValues(t, 1, sends.Load())
	var message models.Message
	require.NoError(t, f.a.DB.First(&message, "id=?", run.ID).Error)
	require.Equal(t, "Reference CASE-NEWER", message.Content)
	_ = driveSequence(t, f, run.ID, 4)
	require.EqualValues(t, 1, sends.Load())
}

func TestInboxSequenceDependenciesTemplatePreviewRechecksApproval(t *testing.T) {
	f := newIntegrationFixture(t)
	field := dependencyField(t, f, "ticket", "text")
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: field.ID}}
	account, _, err := f.a.inboxWorkflowContext(f.a.DB, f.c)
	require.NoError(t, err)
	template := models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.c.OrganizationID, WhatsAppAccount: account.Name, Name: "synthetic_reference", Language: "en", Status: "APPROVED", Category: "UTILITY", HeaderType: "TEXT", HeaderContent: "Case {{1}}", BodyContent: "Reference {{1}}"}
	require.NoError(t, f.a.DB.Create(&template).Error)
	saveDependencyWorkflow(t, &f, []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "send_template", Value: template.ID.String(), ConsentConfirmed: true, Personalise: true, BodyParameters: map[string]string{"1": "{{case.field.ticket}}"}, HeaderParameters: map[string]string{"1": "{{case.field.ticket}}"}}})
	var sends atomic.Int32
	prepareSequenceSender(t, f, nil, &sends)
	preview := dependencyPreview(t, f)
	require.False(t, preview.SendUnavailable)
	require.Equal(t, "Reference …", preview.Reply.Text)
	require.Equal(t, "Case …", preview.Reply.Header)
	run := queueSequenceMacro(t, f)
	require.Equal(t, inboxSequenceReview, driveSequence(t, f, run.ID, 6).State)
	confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	require.NoError(t, f.a.DB.Model(&template).Update("status", "PAUSED").Error)
	require.Equal(t, 409, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
	snapshot, err := f.a.reviewInboxSequence(f.a.DB, f.c.OrganizationID, f.actor.ID, run.ID)
	require.NoError(t, err)
	require.True(t, snapshot.Preview.SendUnavailable)
	require.Equal(t, "template_not_approved", snapshot.Preview.SendUnavailableReason)
	require.Equal(t, "Reference CASE-SEND", snapshot.Preview.Reply.Text)
	require.Zero(t, sends.Load())
}

func TestInboxSequenceDependenciesDoNotDeferIndependentInvalidInputsOrFutureOutputs(t *testing.T) {
	for _, invalid := range []string{"missing_independent", "invalid_static_number", "output_after_input", "self_output", "archived_output"} {
		t.Run(invalid, func(t *testing.T) {
			f := newIntegrationFixture(t)
			field := dependencyField(t, f, "ticket", "text")
			f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: field.ID}}
			second := f.d
			second.ID, second.Outputs = uuid.New(), []models.InboxIntegrationOutput{}
			require.NoError(t, f.a.DB.Create(&second).Error)
			inputs := map[string]string{"customer": "{{case.field.ticket}}", "count": "0", "subscribed": "false"}
			if invalid == "missing_independent" {
				inputs["customer"] += " {{case.field.not_defined}}"
			}
			if invalid == "invalid_static_number" {
				inputs["count"] = "not a number"
			}
			actions := []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "integration", Value: second.ID.String(), IntegrationInputs: inputs}}
			if invalid == "output_after_input" {
				actions[0], actions[1] = actions[1], actions[0]
			}
			if invalid == "self_output" {
				actions = []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: inputs}}
			}
			if invalid == "archived_output" {
				require.NoError(t, f.a.DB.Model(&field).Update("archived_at", time.Now().UTC()).Error)
			}
			saveDependencyWorkflow(t, &f, actions)
			require.False(t, dependencyPreview(t, f).ActionsEligible)
			var executions int64
			require.NoError(t, f.a.DB.Model(&models.InboxIntegrationExecution{}).Where("case_id=?", f.c.ID).Count(&executions).Error)
			require.Zero(t, executions)
		})
	}
}

func TestInboxSequenceDependenciesReturnedTypeCannotBypassNextInputValidation(t *testing.T) {
	f := newIntegrationFixture(t)
	field := dependencyField(t, f, "ticket", "text")
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: field.ID}}
	second := f.d
	second.ID, second.Outputs = uuid.New(), []models.InboxIntegrationOutput{}
	require.NoError(t, f.a.DB.Create(&second).Error)
	inputs := map[string]string{"customer": "Synthetic", "count": "{{case.field.ticket}}", "subscribed": "false"}
	saveDependencyWorkflow(t, &f, []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "integration", Value: second.ID.String(), IntegrationInputs: inputs}})
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"not-a-number"`)}}, nil
	})
	run := queueSequenceMacro(t, f)
	require.Equal(t, inboxSequenceReview, driveSequence(t, f, run.ID, 6).State)
	snapshot, err := f.a.reviewInboxSequence(f.a.DB, f.c.OrganizationID, f.actor.ID, run.ID)
	require.NoError(t, err)
	require.False(t, snapshot.Preview.ActionsEligible)
	require.Equal(t, "inputs_invalid", snapshot.Preview.ActionOutcomes[0].Reason)
	require.Equal(t, 409, resumeSequenceRequest(t, f, f.actor.ID, run.ID, snapshot.Confirmation))
	_ = driveSequence(t, f, run.ID, 4)
	require.EqualValues(t, 1, calls.Load())
}

func TestInboxSequenceDependenciesKeepLaterStagesDeferredAcrossReviews(t *testing.T) {
	f := newIntegrationFixture(t)
	firstField := dependencyField(t, f, "first_ticket", "text")
	secondField := dependencyField(t, f, "second_ticket", "text")
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: firstField.ID}}
	second, third := f.d, f.d
	second.ID, second.Name = uuid.New(), "Synthetic second stage"
	second.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: secondField.ID}}
	third.ID, third.Name, third.Outputs = uuid.New(), "Synthetic third stage", []models.InboxIntegrationOutput{}
	require.NoError(t, f.a.DB.Create(&second).Error)
	require.NoError(t, f.a.DB.Create(&third).Error)
	saveDependencyWorkflow(t, &f, []models.InboxAction{
		{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings},
		{Type: "integration", Value: second.ID.String(), IntegrationInputs: map[string]string{"customer": "{{case.field.first_ticket}}", "count": "0", "subscribed": "false"}},
		{Type: "integration", Value: third.ID.String(), IntegrationInputs: map[string]string{"customer": "{{case.field.second_ticket}}", "count": "0", "subscribed": "false"}},
	})
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(_ context.Context, _ inboxintegration.Endpoint, _, _ string, body []byte, _ string) (inboxintegration.Response, error) {
		index := calls.Add(1)
		values := []string{"FIRST", "SECOND", "THIRD"}
		require.LessOrEqual(t, index, int32(3))
		if index > 1 {
			var request map[string]any
			require.NoError(t, json.Unmarshal(body, &request))
			require.Equal(t, values[index-2], request["customer"])
		}
		raw, err := json.Marshal(values[index-1])
		require.NoError(t, err)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": raw}}, nil
	})
	run := queueSequenceMacro(t, f)
	for stage := 1; stage <= 2; stage++ {
		paused := driveSequence(t, f, run.ID, 7)
		require.Equal(t, inboxSequenceReview, paused.State)
		require.Equal(t, stage, paused.NextAction)
		require.EqualValues(t, stage, calls.Load())
		confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
		require.Equal(t, 200, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
		if stage == 1 {
			require.NoError(t, f.a.DB.First(&paused, "id=?", run.ID).Error)
			plan, err := f.a.inboxSequencePlan(paused)
			require.NoError(t, err)
			require.False(t, plan.DeferredActions[1])
			require.True(t, plan.DeferredActions[2], "reviewed remainder uses absolute cursor positions")
		}
	}
	require.Equal(t, inboxRunApplied, driveSequence(t, f, run.ID, 8).State)
	require.EqualValues(t, 3, calls.Load())
}

func TestInboxSequenceDependenciesPreserveRuleSendReservationAndDisabledReplies(t *testing.T) {
	for _, automaticAllowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "replies_held", true: "one_reserved_reply"}[automaticAllowed], func(t *testing.T) {
			f := newIntegrationFixture(t)
			field := dependencyField(t, f, "ticket", "text")
			f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: field.ID}}
			enabled := time.Now().UTC().Add(-time.Minute)
			f.w.Kind, f.w.Trigger, f.w.EnabledAt = "rule", "incoming_message", &enabled
			saveDependencyWorkflow(t, &f, []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "send_text", Value: "Case {{case.field.ticket}}", Personalise: true}})
			var sends atomic.Int32
			prepareSequenceSender(t, f, nil, &sends)
			require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("inbox_automation_sends_enabled", automaticAllowed).Error)
			later := models.InboxWorkflow{OrganizationID: f.c.OrganizationID, CreatedByID: f.actor.ID, Name: "Later synthetic rule", Kind: "rule", Trigger: "incoming_message", EnabledAt: &enabled, Position: 20, Actions: []models.InboxAction{{Type: "send_text", Value: "Later reply must not dispatch"}, {Type: "note", Value: "Later local work"}}}
			require.NoError(t, f.a.DB.Create(&later).Error)
			event := models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.c.OrganizationID, AccountID: f.c.AccountID, CaseID: f.c.ID, Kind: "incoming_message", WorkflowTrigger: "incoming_message"}
			require.NoError(t, f.a.DB.Create(&event).Error)
			for i := 0; i < 8; i++ {
				require.NoError(t, f.a.runInboxWorkflowCycle(context.Background()))
			}
			var run models.InboxWorkflowRun
			require.NoError(t, f.a.DB.First(&run, "workflow_id=? AND event_id=?", f.w.ID, event.ID).Error)
			require.Zero(t, sends.Load())
			if automaticAllowed {
				require.Equal(t, inboxSequenceReview, run.State)
				require.True(t, run.SequenceSendReserved)
				confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
				require.Equal(t, 200, resumeSequenceRequest(t, f, f.actor.ID, run.ID, confirmation))
			} else {
				require.Equal(t, inboxRunApplied, run.State)
				require.False(t, run.SequenceSendReserved)
			}
			for i := 0; i < 10; i++ {
				require.NoError(t, f.a.runInboxWorkflowCycle(context.Background()))
			}
			require.NoError(t, f.a.processInboxRuleEvent(context.Background(), event))
			require.EqualValues(t, map[bool]int{false: 0, true: 1}[automaticAllowed], sends.Load())
			var stored models.InboxCaseActivity
			require.NoError(t, f.a.DB.First(&stored, "id=?", event.ID).Error)
			require.NotNil(t, stored.WorkflowProcessedAt)
			var laterRun models.InboxWorkflowRun
			require.NoError(t, f.a.DB.First(&laterRun, "workflow_id=? AND event_id=?", later.ID, event.ID).Error)
			if automaticAllowed {
				require.Equal(t, "event_send_already_selected", laterRun.Outcome)
			}
		})
	}
}
