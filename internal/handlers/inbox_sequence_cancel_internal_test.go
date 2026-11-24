package handlers

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
)

func cancellationPreview(t *testing.T, f integrationFixture, id uuid.UUID) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("run_id", id.String())
	require.NoError(t, f.a.PreviewInboxSequenceCancellation(r))
	return r
}

func cancellationInput(t *testing.T, f integrationFixture, id uuid.UUID) map[string]any {
	t.Helper()
	r := cancellationPreview(t, f, id)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	var out struct {
		Eligible     bool   `json:"eligible"`
		Confirmation string `json:"confirmation"`
		Reason       string `json:"reason"`
	}
	testutil.ParseEnvelopeResponse(t, r, &out)
	require.True(t, out.Eligible, out.Reason)
	return map[string]any{"operation_id": uuid.New(), "confirmation": out.Confirmation, "reason": "Stop remaining synthetic workflow work"}
}

func cancellationSubmit(t *testing.T, f integrationFixture, id uuid.UUID, input map[string]any) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("run_id", id.String())
	require.NoError(t, f.a.CancelInboxSequence(r))
	return r
}

func TestInboxSequenceCancellationBeforeDispatchWorksWhileFeatureAndKeyUnavailable(t *testing.T) {
	f := retryFixture(t, 0)
	run := queueSequenceMacro(t, f)
	require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("inbox_workflows_enabled", false).Error)
	f.a.Config.App.EncryptionKey = ""
	input := cancellationInput(t, f, run.ID)
	for i := 0; i < 2; i++ {
		r := cancellationSubmit(t, f, run.ID, input)
		require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	}
	require.NoError(t, f.a.DB.First(&run, "id=?", run.ID).Error)
	require.Equal(t, inboxSequenceCancelled, run.State)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationExecution{}).Where("run_id=?", run.ID).Count(&count).Error)
	require.Zero(t, count)
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("run_id", run.ID.String())
	require.NoError(t, f.a.ListInboxRunRecoveries(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var out struct {
		Items []models.InboxIntegrationRecovery `json:"recoveries"`
	}
	testutil.ParseEnvelopeResponse(t, r, &out)
	require.Len(t, out.Items, 1)
	require.Equal(t, "cancel_remaining", out.Items[0].Kind)
	require.Equal(t, uuid.Nil, out.Items[0].ExecutionID)
	require.NotNil(t, out.Items[0].RunID)
}

func TestInboxSequenceCancellationRetiresOnlyUnsubmittedPendingExecution(t *testing.T) {
	f := retryFixture(t, 0)
	run := queueSequenceMacro(t, f)
	job, err := f.a.advanceInboxSequence(context.Background(), run.ID, f.c.OrganizationID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, job.IntegrationID)
	input := cancellationInput(t, f, run.ID)
	r := cancellationSubmit(t, f, run.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var e models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&e, "id=?", job.IntegrationID).Error)
	require.Equal(t, "cancelled", e.State)
	require.Zero(t, e.AttemptCount)
	require.NotNil(t, e.CompletedAt)
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{}, nil
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), e.ID))
	final := driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxSequenceCancelled, final.State)
	require.Zero(t, calls.Load())
}

func TestInboxSequenceCancellationPreservesInFlightProviderResultAndDoesNotApplyFields(t *testing.T) {
	f := manualFixture(t)
	run := queueSequenceMacro(t, f.integrationFixture)
	job, err := f.a.advanceInboxSequence(context.Background(), run.ID, f.c.OrganizationID)
	require.NoError(t, err)
	started, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		close(started)
		<-release
		return inboxintegration.Response{Status: 200, Fields: manualResult()}, nil
	})
	finished := make(chan error, 1)
	go func() { finished <- f.a.dispatchInboxIntegration(context.Background(), job.IntegrationID) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fake provider did not start")
	}
	r := cancellationPreview(t, f.integrationFixture, run.ID)
	require.Contains(t, string(testutil.GetResponseBody(r)), `"submission_in_progress":true`)
	input := cancellationInput(t, f.integrationFixture, run.ID)
	r = cancellationSubmit(t, f.integrationFixture, run.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	close(release)
	released = true
	require.NoError(t, <-finished)
	var e models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&e, "id=?", job.IntegrationID).Error)
	require.Equal(t, "succeeded", e.State)
	require.Equal(t, "provider", e.ResultOrigin)
	require.Nil(t, e.OutputsAppliedAt)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.c.OrganizationID).Count(&count).Error)
	require.Zero(t, count)
	final := driveSequence(t, f.integrationFixture, run.ID, 3)
	require.Equal(t, inboxSequenceCancelled, final.State)
	require.EqualValues(t, 1, calls.Load())
	applyManualDecision(t, f.integrationFixture, e.ID, inboxIntegrationRecoveryInput{Action: "discard_outputs"})
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.NotNil(t, e.OutputsDiscardedAt)
	final = driveSequence(t, f.integrationFixture, run.ID, 2)
	require.Equal(t, inboxSequenceCancelled, final.State)
}

func TestInboxSequenceCancellationKeepsUnknownOutcomeAndPendingRetryUnresolved(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "pending_retry"}[retry], func(t *testing.T) {
			f := retryFixture(t, 3600)
			run, e := unknownRetry(t, f)
			if retry {
				confirmation := retryConfirmation(t, f, e.ID)
				r := retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), confirmation, "Explicit retry not yet dispatched")
				require.Equal(t, 200, testutil.GetResponseStatusCode(r))
			}
			r := cancellationSubmit(t, f, run.ID, cancellationInput(t, f, run.ID))
			require.Equal(t, 200, testutil.GetResponseStatusCode(r))
			require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
			require.Equal(t, "unknown", e.State)
			require.Equal(t, 1, e.AttemptCount)
			require.False(t, e.RetryRequested)
			applyManualDecision(t, f, e.ID, inboxIntegrationRecoveryInput{Action: "confirm_not_applied", Reference: "CANCELLED-OPERATION-CHECKED"})
			final := driveSequence(t, f, run.ID, 2)
			require.Equal(t, inboxSequenceCancelled, final.State)
			require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
			require.Equal(t, "failed", e.State)
			require.Equal(t, "manual", e.ResultOrigin)
		})
	}
}

func TestInboxSequenceCancellationDoesNotHideUnknownWhatsAppSubmission(t *testing.T) {
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "send_text", Value: "Synthetic reply with lost submission response"}, {Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}}
	})
	var calls atomic.Int32
	prepareSequenceSender(t, f, nil, &calls)
	f.a.WhatsApp.HTTPClient.Transport = inboxFixtureTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("synthetic submission response lost")
	})
	run := queueSequenceMacro(t, f)
	unknown := driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxSequenceUnknown, unknown.State)
	require.EqualValues(t, 1, calls.Load())
	r := cancellationPreview(t, f, run.ID)
	require.Contains(t, string(testutil.GetResponseBody(r)), "reconcile_message_submission_first")
	var out struct {
		Eligible bool `json:"eligible"`
	}
	testutil.ParseEnvelopeResponse(t, r, &out)
	require.False(t, out.Eligible)
	var decisions int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("run_id=?", run.ID).Count(&decisions).Error)
	require.Zero(t, decisions)
}

func TestInboxSequenceCancellationRechecksScopeAndChangedSubmissionState(t *testing.T) {
	f := retryFixture(t, 0)
	run := queueSequenceMacro(t, f)
	original := f.actor
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Scoped cancellation operator", []string{"chat:read", "chat:write", "settings.general:write", "custom_actions:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", agent.ID).Error)
	f.actor = agent
	input := cancellationInput(t, f, run.ID)
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", original.ID).Error)
	r := cancellationSubmit(t, f, run.ID, input)
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	f.actor = original
	paused := driveSequence(t, f, run.ID, 1)
	require.Equal(t, inboxSequenceReview, paused.State)
	review := sequenceReviewRequest(t, f, original.ID, run.ID)
	require.Equal(t, 200, resumeSequenceRequest(t, f, original.ID, run.ID, review))
	input = cancellationInput(t, f, run.ID)
	job, err := f.a.advanceInboxSequence(context.Background(), run.ID, f.c.OrganizationID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, job.IntegrationID)
	r = cancellationSubmit(t, f, run.ID, input)
	require.Equal(t, 409, testutil.GetResponseStatusCode(r), "a newly queued/submitted step requires updated cancellation review")
	r = cancellationSubmit(t, f, run.ID, cancellationInput(t, f, run.ID))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
}
