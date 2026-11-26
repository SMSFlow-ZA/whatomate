package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxIntegrationRetryPreview struct {
	Execution    models.InboxIntegrationExecution
	Run          models.InboxWorkflowRun
	Case         models.InboxCase
	Prepared     preparedInboxIntegration
	Confirmation string
	Reason       string
}

func (a *App) integrationRecoveryAuth(r *fastglue.Request) (uuid.UUID, uuid.UUID, bool) {
	return a.integrationOperatorAuth(r, false)
}

func (a *App) integrationOperatorAuth(r *fastglue.Request, allowDisabled bool) (uuid.UUID, uuid.UUID, bool) {
	var org, actor uuid.UUID
	var ok bool
	if allowDisabled {
		var err error
		org, actor, err = a.requireInboxAuth(r, models.ResourceChat, models.ActionWrite)
		ok = err == nil
	} else {
		org, actor, ok = a.inboxFeatureAuth(r, models.ActionWrite)
	}
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	if _, _, err := a.requireInboxAuth(r, models.ResourceSettingsGeneral, models.ActionWrite); err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	if _, _, err := a.requireInboxAuth(r, models.ResourceCustomActions, models.ActionWrite); err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	return org, actor, true
}

func (a *App) previewInboxIntegrationRetry(db *gorm.DB, org, actor, id uuid.UUID, now time.Time) (inboxIntegrationRetryPreview, error) {
	var out inboxIntegrationRetryPreview
	if err := a.scopedInboxIntegrationHistory(db, org, actor).First(&out.Execution, "id=?", id).Error; err != nil {
		return out, err
	}
	e := out.Execution
	if err := db.First(&out.Run, "id=? AND organization_id=?", e.RunID, org).Error; err != nil {
		return out, err
	}
	run := out.Run
	if run.ActorID != e.ActorID || run.CaseID != e.CaseID || run.AccountID != e.AccountID || run.ContactID != e.ContactID {
		out.Reason = "workflow_context_changed"
		return out, nil
	}
	if err := a.scopeInboxCases(db.Model(&models.InboxCase{}), actor, org).First(&out.Case, "inbox_cases.id=?", e.CaseID).Error; err != nil {
		return out, err
	}
	c := out.Case
	if e.State != "unknown" && e.State != "failed" {
		out.Reason = "not_retryable_state"
		return out, nil
	}
	if !run.Sequenced || run.NextAction != e.ActionIndex || (run.State != inboxSequenceUnknown && run.State != inboxSequenceBlocked && run.State != inboxSequenceRunning) {
		out.Reason = "workflow_stopped"
		return out, nil
	}
	if e.AttemptCount < 1 || e.StartedAt == nil || e.RetryUntil == nil || !now.Add(35*time.Second).Before(*e.RetryUntil) {
		out.Reason = "idempotency_window_unavailable"
		return out, nil
	}
	latest, err := (inboxThread{org, e.AccountID, e.ContactID}).latest(db)
	if err != nil {
		return out, err
	}
	if latest.ID != e.CaseID {
		out.Reason = "case_changed"
		return out, nil
	}
	digest, err := inboxSendCaseDigest(db, c)
	if err != nil {
		return out, err
	}
	if digest != e.CaseDigest || digest != run.CaseDigest {
		out.Reason = "case_changed"
		return out, nil
	}
	var w models.InboxWorkflow
	if err := a.visibleInboxWorkflows(db, org, actor).First(&w, "id=?", run.WorkflowID).Error; err != nil {
		return out, err
	}
	if !w.UpdatedAt.Equal(run.WorkflowUpdatedAt) || e.ActionIndex < 0 || e.ActionIndex >= len(w.Actions) || (w.Kind == "rule" && w.EnabledAt == nil) {
		out.Reason = "workflow_changed"
		return out, nil
	}
	action := w.Actions[e.ActionIndex]
	if action.Type != "integration" || action.Value != e.IntegrationID.String() {
		out.Reason = "action_changed"
		return out, nil
	}
	snapshot, err := a.decodeIntegrationRequest(e)
	if err != nil {
		out.Reason = "snapshot_unavailable"
		return out, nil
	}
	if workflowDigest(snapshot.Mappings) != workflowDigest(action.IntegrationInputs) {
		out.Reason = "action_changed"
		return out, nil
	}
	// The original actor remains the execution identity. Recovery does not
	// silently render a different agent name or adopt broader dispatch rights.
	prepared, err := a.prepareInboxIntegration(db, c, e.ActorID, e.IntegrationID, snapshot.Mappings)
	if err != nil {
		out.Reason = integrationProblemReason(err)
		return out, nil
	}
	if actor != e.ActorID && !a.HasPermission(actor, models.ResourceContacts, models.ActionRead, org) {
		out.Reason = "input_access_required"
		return out, nil
	}
	if _, err := a.integrationOutputFields(db, prepared.Definition, actor); err != nil {
		out.Reason = integrationProblemReason(err)
		return out, nil
	}
	if !prepared.Intent.Available {
		out.Reason = prepared.Intent.Reason
		return out, nil
	}
	if prepared.Endpoint.IdempotencyHeader == "" || prepared.Endpoint.IdempotencyRetentionSeconds < 60 || prepared.DefinitionDigest != e.DefinitionDigest || prepared.RequestDigest != e.RequestDigest || workflowDigest(json.RawMessage(prepared.Body)) != workflowDigest(json.RawMessage(snapshot.Body)) {
		out.Reason = "definition_or_values_changed"
		return out, nil
	}
	out.Prepared = prepared
	out.Confirmation = inboxPreviewConfirmation([]any{"integration-retry", actor, e.ID, e.State, e.Outcome, e.UpdatedAt, e.AttemptCount, e.DefinitionDigest, e.RequestDigest, e.CaseDigest, e.RetryUntil, run.ID, run.State, run.NextAction, run.ActorID})
	return out, nil
}

func (a *App) PreviewInboxIntegrationRetry(r *fastglue.Request) error {
	org, actor, ok := a.integrationRecoveryAuth(r)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "execution_id", "Integration execution")
	if err != nil {
		return nil
	}
	preview, err := a.previewInboxIntegrationRetry(a.DB, org, actor, id, time.Now().UTC())
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"execution_id": id, "eligible": preview.Confirmation != "", "reason": preview.Reason, "confirmation": preview.Confirmation, "retry_until": preview.Execution.RetryUntil, "intent": preview.Prepared.Intent, "same_operation": true})
}

func (a *App) RetryInboxIntegration(r *fastglue.Request) error {
	org, actor, ok := a.integrationRecoveryAuth(r)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "execution_id", "Integration execution")
	if err != nil {
		return nil
	}
	var input struct {
		OperationID  uuid.UUID `json:"operation_id"`
		Confirmation string    `json:"confirmation"`
		Reason       string    `json:"reason"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.OperationID == uuid.Nil || len(input.Confirmation) != 64 || input.Reason == "" || utf8.RuneCountInString(input.Reason) > 1000 || strings.ContainsAny(input.Reason, "\x00\r") {
		return a.inboxError(r, errInboxTransition)
	}
	requestDigest := workflowDigest([]any{"retry", org, actor, id, input.Confirmation, input.Reason})
	var recovery models.InboxIntegrationRecovery
	responseState := ""
	// Persist the bounded decision even if the browser disconnects; its
	// operation ID makes an uncertain HTTP acknowledgement safe to replay.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, org, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxDisabled
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-integration-recovery:"+input.OperationID.String()).Error; err != nil {
			return err
		}
		// Even a replay must pass today's history/case permissions.
		var execution models.InboxIntegrationExecution
		if err := a.scopedInboxIntegrationHistory(tx, org, actor).First(&execution, "id=?", id).Error; err != nil {
			return err
		}
		if err := a.scopeInboxCases(tx.Model(&models.InboxCase{}), actor, org).First(&models.InboxCase{}, "inbox_cases.id=?", execution.CaseID).Error; err != nil {
			return err
		}
		responseState = execution.State
		err = tx.First(&recovery, "id=?", input.OperationID).Error
		if err == nil {
			if recovery.OrganizationID != org || recovery.ExecutionID != id || recovery.ActorID != actor || recovery.RequestDigest != requestDigest {
				return errInboxConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// A finisher takes execution -> organisation. Do not wait on execution
		// while holding the organisation share lock.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&execution, "id=? AND organization_id=?", id, org).Error; err != nil {
			var pgError *pgconn.PgError
			if errors.As(err, &pgError) && pgError.Code == "55P03" {
				return errInboxConflict
			}
			return err
		}
		var run models.InboxWorkflowRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "id=? AND organization_id=?", execution.RunID, org).Error; err != nil {
			return err
		}
		if !run.Sequenced || (run.State != inboxSequenceUnknown && run.State != inboxSequenceBlocked && run.State != inboxSequenceRunning) {
			return errInboxConflict
		}
		if err := (inboxThread{org, execution.AccountID, execution.ContactID}).lock(tx); err != nil {
			return err
		}
		preview, err := a.previewInboxIntegrationRetry(tx, org, actor, id, time.Now().UTC())
		if err != nil {
			return err
		}
		if preview.Confirmation == "" || !inboxPreviewMatches(preview.Confirmation, input.Confirmation) {
			return errInboxConflict
		}
		key, err := a.integrationEncryptionKey()
		if err != nil {
			return err
		}
		credential, err := crypto.Decrypt(preview.Prepared.Definition.Credential, key)
		if preview.Prepared.Definition.AuthMode != "none" && err != nil {
			return errInboxTransition
		}
		endpointURL, err := url.Parse(preview.Prepared.Endpoint.URL)
		if err != nil {
			return errInboxTransition
		}
		if credential != "" && strings.Contains(input.Reason, credential) || strings.Contains(input.Reason, preview.Prepared.Endpoint.URL) || endpointURL.Path != "" && endpointURL.Path != "/" && strings.Contains(input.Reason, endpointURL.Path) {
			return errInboxTransition
		}
		recovery = models.InboxIntegrationRecovery{BaseModel: models.BaseModel{ID: input.OperationID}, OrganizationID: org, ExecutionID: id, ActorID: actor, Kind: "retry", FromState: execution.State, FromOutcome: execution.Outcome, AttemptCount: execution.AttemptCount, Reason: input.Reason, RequestDigest: requestDigest}
		recovery.PriorStartedAt, recovery.PriorCompletedAt, recovery.ProviderStatus, recovery.DurationMillis = execution.StartedAt, execution.CompletedAt, execution.ProviderStatus, execution.DurationMillis
		runID := execution.RunID
		recovery.RunID, recovery.FromResultOrigin = &runID, execution.ResultOrigin
		if err := tx.Create(&recovery).Error; err != nil {
			return err
		}
		// Retain exact request, operation ID, actor and immutable expiry. This
		// queues work only; it never enables a dispatch switch or calls HTTP.
		if err := tx.Model(&execution).Updates(map[string]any{"state": "pending", "outcome": "", "retry_requested": true, "result_origin": "", "started_at": nil, "completed_at": nil, "provider_status": 0, "duration_millis": 0, "reconciled_by_id": actor, "reconciliation_reason": input.Reason}).Error; err != nil {
			return err
		}
		if run.NextAction < 0 || run.NextAction >= len(run.ActionOutcomes) {
			return errInboxConflict
		}
		run.State, run.Outcome = inboxSequenceRunning, ""
		run.ActionOutcomes[run.NextAction].Status, run.ActionOutcomes[run.NextAction].Reason = "pending", "explicit_idempotent_retry"
		if err := saveInboxSequence(tx, &run); err != nil {
			return err
		}
		responseState = "pending"
		return appendInboxActivity(tx, preview.Case, models.InboxCaseActivity{Kind: "integration_retry_requested", ActorID: &actor, Detail: preview.Prepared.Definition.Name})
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"recovery": recovery, "queued": responseState == "pending", "state": responseState})
}
