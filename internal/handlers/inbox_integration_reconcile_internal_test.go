package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type manualOutputFixture struct {
	integrationFixture
	Text, Count, Flag models.InboxFieldDefinition
}

func manualFixture(t *testing.T) manualOutputFixture {
	t.Helper()
	f := retryFixture(t, 0)
	fields := []models.InboxFieldDefinition{
		{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_label", Label: "External label", Kind: "text"},
		{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "external_total", Label: "External total", Kind: "number"},
		{OrganizationID: f.c.OrganizationID, Scope: "contact", Key: "external_member", Label: "External member", Kind: "boolean"},
	}
	for i := range fields {
		require.NoError(t, f.a.DB.Create(&fields[i]).Error)
	}
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "label", DefinitionID: fields[0].ID}, {Name: "count", DefinitionID: fields[1].ID}, {Name: "flag", DefinitionID: fields[2].ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	return manualOutputFixture{f, fields[0], fields[1], fields[2]}
}

func manualResult() map[string]json.RawMessage {
	return map[string]json.RawMessage{"reference": json.RawMessage(`"EXTERNAL-42"`), "label": json.RawMessage(`"Verified external label"`), "count": json.RawMessage(`0`), "flag": json.RawMessage(`false`)}
}

func manualPreview(t *testing.T, f integrationFixture, actor, id uuid.UUID, input inboxIntegrationRecoveryInput) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.c.OrganizationID, actor)
	r.RequestCtx.SetUserValue("execution_id", id.String())
	require.NoError(t, f.a.PreviewInboxIntegrationRecovery(r))
	return r
}

func manualConfirmation(t *testing.T, f integrationFixture, actor, id uuid.UUID, input inboxIntegrationRecoveryInput) string {
	t.Helper()
	r := manualPreview(t, f, actor, id, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	var out struct {
		Eligible     bool   `json:"eligible"`
		Confirmation string `json:"confirmation"`
		Reason       string `json:"reason"`
	}
	testutil.ParseEnvelopeResponse(t, r, &out)
	require.True(t, out.Eligible, out.Reason)
	require.NotContains(t, string(testutil.GetResponseBody(r)), "synthetic-secret")
	require.NotContains(t, string(testutil.GetResponseBody(r)), "encrypted_result")
	return out.Confirmation
}

func manualSubmit(t *testing.T, f integrationFixture, actor, id uuid.UUID, input inboxIntegrationRecoveryInput) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.c.OrganizationID, actor)
	r.RequestCtx.SetUserValue("execution_id", id.String())
	require.NoError(t, f.a.RecoverInboxIntegration(r))
	return r
}

func applyManualDecision(t *testing.T, f integrationFixture, id uuid.UUID, input inboxIntegrationRecoveryInput) inboxIntegrationRecoveryInput {
	t.Helper()
	input.OperationID = uuid.New()
	input.Confirmation = manualConfirmation(t, f, f.actor.ID, id, input)
	input.Reason = "Provider investigation and mapped changes reviewed"
	r := manualSubmit(t, f, f.actor.ID, id, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	return input
}

func TestInboxIntegrationManualRecoverySeparatesFactAndAtomicOutputReview(t *testing.T) {
	f := manualFixture(t)
	run, e := unknownRetry(t, f.integrationFixture)
	before := e
	f.a.Config.InboxAutomation.IntegrationDispatchEnabled = false
	require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("settings", models.JSONB{"inbox_integrations_enabled": false}).Error)
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{}, nil
	})
	input := applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "INVESTIGATION-42", Result: manualResult()})
	r := manualSubmit(t, f.integrationFixture, f.actor.ID, e.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.Zero(t, count, "recording success must not write fields")
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.Equal(t, "succeeded", e.State)
	require.Equal(t, "manual", e.ResultOrigin)
	require.True(t, crypto.IsEncrypted(e.EncryptedResult))
	require.Nil(t, e.OutputsAppliedAt)
	require.Zero(t, e.ProviderStatus, "a manual attestation is not a fabricated provider HTTP response")
	local := models.InboxFieldValue{OrganizationID: f.c.OrganizationID, DefinitionID: f.Text.ID, SubjectID: f.c.ID, Value: json.RawMessage(`"Local newer value"`)}
	require.NoError(t, f.a.DB.Create(&local).Error)
	preview := inboxIntegrationRecoveryInput{Action: "apply_outputs"}
	preview.Confirmation = manualConfirmation(t, f.integrationFixture, f.actor.ID, e.ID, preview)
	preview.OperationID, preview.Reason = uuid.New(), "Reviewed actual current and proposed values"
	require.NoError(t, f.a.DB.Model(&local).Update("value", json.RawMessage(`"Edited after preview"`)).Error)
	r = manualSubmit(t, f.integrationFixture, f.actor.ID, e.ID, preview)
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.EqualValues(t, 1, count, "stale apply must not partially add other fields")
	preview.Confirmation = manualConfirmation(t, f.integrationFixture, f.actor.ID, e.ID, preview)
	r = manualSubmit(t, f.integrationFixture, f.actor.ID, e.ID, preview)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	r = manualSubmit(t, f.integrationFixture, f.actor.ID, e.ID, preview)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var values []models.InboxFieldValue
	require.NoError(t, f.a.DB.Where("organization_id=?", f.c.OrganizationID).Find(&values).Error)
	require.Len(t, values, 3)
	for _, value := range values {
		switch value.DefinitionID {
		case f.Text.ID:
			require.JSONEq(t, `"Verified external label"`, string(value.Value))
		case f.Count.ID:
			require.JSONEq(t, `0`, string(value.Value))
		case f.Flag.ID:
			require.JSONEq(t, `false`, string(value.Value))
			require.Equal(t, f.c.ContactID, value.SubjectID)
		}
	}
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.Equal(t, before.ActorID, e.ActorID)
	require.Equal(t, before.CaseDigest, e.CaseDigest)
	require.Equal(t, before.EncryptedRequest, e.EncryptedRequest)
	require.NotNil(t, e.OutputsAppliedAt)
	require.Nil(t, e.OutputsDiscardedAt)
	var currentCase models.InboxCase
	require.NoError(t, f.a.DB.First(&currentCase, "id=?", f.c.ID).Error)
	require.Equal(t, f.c.Version+1, currentCase.Version, "replayed output application must not increment the case twice")
	paused := driveSequence(t, f.integrationFixture, run.ID, 3)
	require.Equal(t, inboxSequenceReview, paused.State)
	require.Equal(t, 1, paused.NextAction)
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND detail=?", f.c.ID, "After recovered operation").Count(&count).Error)
	require.Zero(t, count, "manual recovery holds all remaining work")
	require.Zero(t, calls.Load())
	require.False(t, f.a.Config.InboxAutomation.IntegrationDispatchEnabled)
	var decision models.InboxIntegrationRecovery
	require.NoError(t, f.a.DB.First(&decision, "id=?", input.OperationID).Error)
	require.True(t, crypto.IsEncrypted(decision.EncryptedEvidence))
	require.NotContains(t, decision.EncryptedEvidence, "INVESTIGATION-42")
	evidence := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(evidence, f.c.OrganizationID, f.actor.ID)
	evidence.RequestCtx.SetUserValue("execution_id", e.ID.String())
	evidence.RequestCtx.SetUserValue("recovery_id", decision.ID.String())
	require.NoError(t, f.a.GetInboxIntegrationRecoveryEvidence(evidence))
	require.Equal(t, 200, testutil.GetResponseStatusCode(evidence))
	require.Contains(t, string(testutil.GetResponseBody(evidence)), "INVESTIGATION-42")
	require.Contains(t, string(testutil.GetResponseBody(evidence)), "manual_attestation")
	// An explicit new review resumes only the remaining local note.
	f.a.Config.InboxAutomation.IntegrationDispatchEnabled = true
	require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("settings", models.JSONB{"inbox_integrations_enabled": true}).Error)
	confirmation := sequenceReviewRequest(t, f.integrationFixture, f.actor.ID, run.ID)
	require.Equal(t, 200, resumeSequenceRequest(t, f.integrationFixture, f.actor.ID, run.ID, confirmation))
	final := driveSequence(t, f.integrationFixture, run.ID, 3)
	require.Equal(t, inboxRunApplied, final.State)
	require.Zero(t, calls.Load())
}

func TestInboxIntegrationManualRecoveryNoEffectCannotBypassNoRetry(t *testing.T) {
	f := retryFixture(t, 0)
	run, e := unknownRetry(t, f)
	applyManualDecision(t, f, e.ID, inboxIntegrationRecoveryInput{Action: "confirm_not_applied", Reference: "PROVIDER-NO-EFFECT-1"})
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.Equal(t, "failed", e.State)
	require.Equal(t, "manual", e.ResultOrigin)
	require.Nil(t, e.RetryUntil)
	paused := driveSequence(t, f, run.ID, 2)
	require.Equal(t, inboxSequenceBlocked, paused.State)
	r := retryPreviewRequest(t, f, f.actor.ID, e.ID)
	require.Contains(t, string(testutil.GetResponseBody(r)), "idempotency_window_unavailable")
	r = retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), strings.Repeat("f", 64), "Manual claim cannot grant idempotency")
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
}

func TestInboxIntegrationManualRecoveryRejectsInvalidAndSecretValues(t *testing.T) {
	f := manualFixture(t)
	_, e := unknownRetry(t, f.integrationFixture)
	for _, kind := range []string{"unknown", "wrong_type", "credential", "missing_reference", "secret_reference", "no_effect_with_values"} {
		t.Run(kind, func(t *testing.T) {
			input := inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "INVESTIGATION-SAFE", Result: manualResult()}
			switch kind {
			case "unknown":
				input.Result["unexpected"] = json.RawMessage(`"not allowed"`)
			case "wrong_type":
				input.Result["count"] = json.RawMessage(`"not a number"`)
			case "credential":
				input.Result["label"] = json.RawMessage(`"synthetic-secret"`)
			case "missing_reference":
				input.Reference = ""
			case "secret_reference":
				input.Reference = "synthetic-secret"
			case "no_effect_with_values":
				input.Action = "confirm_not_applied"
			}
			r := manualPreview(t, f.integrationFixture, f.actor.ID, e.ID, input)
			require.Equal(t, 200, testutil.GetResponseStatusCode(r))
			var out struct {
				Eligible bool `json:"eligible"`
			}
			testutil.ParseEnvelopeResponse(t, r, &out)
			require.False(t, out.Eligible)
			require.NotContains(t, string(testutil.GetResponseBody(r)), "synthetic-secret")
		})
	}
	input := inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "SAFE-REFERENCE", Result: manualResult(), OperationID: uuid.New(), Reason: "Do not store synthetic-secret"}
	input.Confirmation = manualConfirmation(t, f.integrationFixture, f.actor.ID, e.ID, input)
	r := manualSubmit(t, f.integrationFixture, f.actor.ID, e.ID, input)
	require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("execution_id=?", e.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntegrationManualRecoveryAfterDefinitionChangeCanRecordOnlyFact(t *testing.T) {
	f := manualFixture(t)
	run, e := unknownRetry(t, f.integrationFixture)
	require.NoError(t, f.a.DB.Model(&f.d).Update("description", "Changed after submission").Error)
	input := inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "OLD-OPERATION-FOUND", Result: manualResult()}
	r := manualPreview(t, f.integrationFixture, f.actor.ID, e.ID, input)
	require.Contains(t, string(testutil.GetResponseBody(r)), "definition_changed_record_fact_only")
	input.Result = nil
	applyManualDecision(t, f.integrationFixture, e.ID, input)
	r = manualPreview(t, f.integrationFixture, f.actor.ID, e.ID, inboxIntegrationRecoveryInput{Action: "apply_outputs"})
	require.Contains(t, string(testutil.GetResponseBody(r)), "definition_changed")
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "discard_outputs"})
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.NotNil(t, e.OutputsDiscardedAt)
	require.Nil(t, e.OutputsAppliedAt)
	require.Empty(t, e.EncryptedResult)
	paused := driveSequence(t, f.integrationFixture, run.ID, 2)
	require.Equal(t, inboxSequenceReview, paused.State)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntegrationManualRecoveryDoesNotWaitOnLockedExecution(t *testing.T) {
	f := retryFixture(t, 0)
	_, e := unknownRetry(t, f)
	input := inboxIntegrationRecoveryInput{Action: "confirm_not_applied", Reference: "CHECKED-NO-EFFECT", OperationID: uuid.New(), Reason: "Exact provider operation reviewed"}
	input.Confirmation = manualConfirmation(t, f, f.actor.ID, e.ID, input)
	locked := f.a.DB.Begin()
	require.NoError(t, locked.Error)
	defer locked.Rollback()
	require.NoError(t, locked.Clauses(clause.Locking{Strength: "UPDATE"}).First(&models.InboxIntegrationExecution{}, "id=?", e.ID).Error)
	started := time.Now()
	r := manualSubmit(t, f, f.actor.ID, e.ID, input)
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	require.Less(t, time.Since(started), 2*time.Second)
}

func TestInboxIntegrationManualRecoveryReviewsGenuineProviderContextConflict(t *testing.T) {
	f := manualFixture(t)
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		require.NoError(t, f.a.DB.Create(&models.InboxFieldValue{OrganizationID: f.c.OrganizationID, DefinitionID: f.Count.ID, SubjectID: f.c.ID, Value: json.RawMessage(`99`)}).Error)
		return inboxintegration.Response{Status: 200, Fields: manualResult()}, nil
	})
	run := queueSequenceMacro(t, f.integrationFixture)
	paused := driveSequence(t, f.integrationFixture, run.ID, 3)
	require.Equal(t, inboxSequenceReview, paused.State)
	var e models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&e, "run_id=?", run.ID).Error)
	require.Equal(t, "provider", e.ResultOrigin)
	require.Equal(t, "output_context_changed", e.Outcome)
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "apply_outputs"})
	require.EqualValues(t, 1, calls.Load())
	paused = driveSequence(t, f.integrationFixture, run.ID, 2)
	require.Equal(t, inboxSequenceReview, paused.State)
	require.Equal(t, 1, paused.NextAction)
	require.EqualValues(t, 1, calls.Load())
}

func TestInboxIntegrationManualRecoveryMasksOutputValuesAndRejectsHiddenApplication(t *testing.T) {
	f := manualFixture(t)
	_, e := unknownRetry(t, f.integrationFixture)
	require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("settings", models.JSONB{"inbox_integrations_enabled": true, "mask_phone_numbers": true}).Error)
	values := manualResult()
	values["label"] = json.RawMessage(`"+12025550123"`)
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "MASKED-RESULT-1", Result: values})
	r := manualPreview(t, f.integrationFixture, f.actor.ID, e.ID, inboxIntegrationRecoveryInput{Action: "apply_outputs"})
	require.NotContains(t, string(testutil.GetResponseBody(r)), "+12025550123")
	require.Contains(t, string(testutil.GetResponseBody(r)), "masked_output_requires_authorised_review")
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "discard_outputs"})
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntegrationManualRecoveryScopeAndArchivedFieldsRemainEnforced(t *testing.T) {
	f := manualFixture(t)
	_, e := unknownRetry(t, f.integrationFixture)
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "SCOPED-RESULT-1", Result: manualResult()})
	input := inboxIntegrationRecoveryInput{Action: "apply_outputs", OperationID: uuid.New(), Reason: "Apply permitted reviewed fields"}
	input.Confirmation = manualConfirmation(t, f.integrationFixture, f.actor.ID, e.ID, input)
	require.NoError(t, f.a.DB.Model(&f.Flag).Update("archived_at", time.Now().UTC()).Error)
	r := manualSubmit(t, f.integrationFixture, f.actor.ID, e.ID, input)
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.Zero(t, count)
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Recovery restricted operator", []string{"chat:read", "chat:write", "custom_actions:write", "settings.general:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", agent.ID).Error)
	r = manualPreview(t, f.integrationFixture, agent.ID, e.ID, inboxIntegrationRecoveryInput{Action: "apply_outputs"})
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NotContains(t, string(testutil.GetResponseBody(r)), "Verified external label")
	foreign := retryFixture(t, 0)
	_, other := unknownRetry(t, foreign)
	r = manualPreview(t, f.integrationFixture, f.actor.ID, other.ID, inboxIntegrationRecoveryInput{Action: "confirm_not_applied", Reference: "FOREIGN"})
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	// Losing the current thread assignment also revokes recovery history access.
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", f.actor.ID).Error)
	r = manualPreview(t, f.integrationFixture, agent.ID, e.ID, inboxIntegrationRecoveryInput{Action: "discard_outputs"})
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
}

func TestInboxIntegrationManualRecoveryConcurrentApplicationsCommitOneFieldBatch(t *testing.T) {
	f := manualFixture(t)
	_, e := unknownRetry(t, f.integrationFixture)
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "CONCURRENT-1", Result: manualResult()})
	base := inboxIntegrationRecoveryInput{Action: "apply_outputs", Reason: "Apply one signed field batch"}
	base.Confirmation = manualConfirmation(t, f.integrationFixture, f.actor.ID, e.ID, base)
	requests := make([]*fastglue.Request, 4)
	for i := range requests {
		input := base
		input.OperationID = uuid.New()
		r := testutil.NewJSONRequest(t, input)
		testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
		r.RequestCtx.SetUserValue("execution_id", e.ID.String())
		requests[i] = r
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, r := range requests {
		wg.Add(1)
		go func(r *fastglue.Request) { defer wg.Done(); errs <- f.a.RecoverInboxIntegration(r) }(r)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	success := 0
	for _, r := range requests {
		code := testutil.GetResponseStatusCode(r)
		require.Contains(t, []int{200, 409}, code, string(testutil.GetResponseBody(r)))
		if code == 200 {
			success++
		}
	}
	require.Equal(t, 1, success)
	var c models.InboxCase
	require.NoError(t, f.a.DB.First(&c, "id=?", f.c.ID).Error)
	require.Equal(t, f.c.Version+1, c.Version)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("execution_id=? AND kind='apply_outputs'", e.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxIntegrationManualRecoverySupervisorUsesCurrentRightsAndPreservesOriginalActor(t *testing.T) {
	f := manualFixture(t)
	run, e := unknownRetry(t, f.integrationFixture)
	originalActor := f.actor
	supervisor := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithSuperAdmin())
	require.NoError(t, f.a.DB.Model(originalActor).Update("is_active", false).Error)
	f.actor = supervisor
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "confirm_succeeded", Reference: "SUPERVISOR-INVESTIGATION", Result: manualResult()})
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "apply_outputs"})
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.Equal(t, originalActor.ID, e.ActorID)
	require.NotNil(t, e.ReconciledByID)
	require.Equal(t, supervisor.ID, *e.ReconciledByID)
	require.NoError(t, f.a.DB.First(&run, "id=?", run.ID).Error)
	require.Equal(t, supervisor.ID, run.ActorID)
	require.NotNil(t, run.InitiatedByID)
	require.Equal(t, originalActor.ID, *run.InitiatedByID)
	require.Equal(t, inboxSequenceReview, run.State)
}

func TestInboxIntegrationManualRecoveryPersistenceErrorsDoNotExposeProviderData(t *testing.T) {
	marker := "synthetic provider content and credential"
	pgError := &pgconn.PgError{Code: "23514", Message: marker, Detail: marker, ConstraintName: marker}
	for _, err := range []error{pgError, fmt.Errorf("%s: %w", marker, pgError), errors.New(marker)} {
		safe := inboxIntegrationPersistenceError(err)
		require.Error(t, safe)
		require.NotContains(t, safe.Error(), marker)
	}
	require.Contains(t, inboxIntegrationPersistenceError(pgError).Error(), "23514")
	require.ErrorIs(t, inboxIntegrationPersistenceError(fmt.Errorf("%s: %w", marker, errInboxConflict)), errInboxConflict)
	require.NotContains(t, inboxIntegrationPersistenceError(fmt.Errorf("%s: %w", marker, context.DeadlineExceeded)).Error(), marker)
}

func TestInboxIntegrationManualRecoverySQLDebugAndConstraintFailureDoNotLogValues(t *testing.T) {
	f := manualFixture(t)
	run := queueSequenceMacro(t, f.integrationFixture)
	job, err := f.a.advanceInboxSequence(context.Background(), run.ID, f.c.OrganizationID)
	require.NoError(t, err)
	constraint := "synthetic_output_rejection_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, f.a.DB.Exec("ALTER TABLE inbox_field_values ADD CONSTRAINT "+constraint+" CHECK (organization_id <> '"+f.c.OrganizationID.String()+"'::uuid)").Error)
	t.Cleanup(func() {
		require.NoError(t, f.a.DB.Exec("ALTER TABLE inbox_field_values DROP CONSTRAINT "+constraint).Error)
	})
	var buffer bytes.Buffer
	f.a.DB = f.a.DB.Session(&gorm.Session{Logger: logger.New(log.New(&buffer, "", 0), logger.Config{LogLevel: logger.Info})})
	var check int
	require.NoError(t, f.a.DB.Raw("SELECT 1").Scan(&check).Error)
	require.Equal(t, 1, check)
	marker := "SYNTHETIC-PROVIDER-RESPONSE-DO-NOT-LOG"
	values := manualResult()
	raw, _ := json.Marshal(marker)
	values["label"] = raw
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{Status: 200, Fields: values}, nil
	})
	err = f.a.dispatchInboxIntegration(context.Background(), job.IntegrationID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "23514")
	require.NotContains(t, err.Error(), marker)
	require.Contains(t, buffer.String(), "SELECT", "the application debug logger must really be active")
	require.NotContains(t, buffer.String(), marker)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.Zero(t, count)
	var e models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&e, "id=?", job.IntegrationID).Error)
	require.Equal(t, "submitting", e.State, "failed result persistence remains held rather than replayed")
	require.NoError(t, f.a.reconcileInterruptedInboxIntegrations(context.Background(), time.Now().UTC().Add(3*time.Minute)))
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.Equal(t, "unknown", e.State)
}
