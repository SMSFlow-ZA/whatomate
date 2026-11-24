package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type integrationRequestSnapshot struct {
	Mappings map[string]string `json:"mappings"`
	Body     json.RawMessage   `json:"body"`
}

// Enqueue only persists work in the caller's transaction. Network I/O belongs
// after commit. The workflow bridge supplies the reviewed, scoped preparation.
func (a *App) enqueueInboxIntegration(tx *gorm.DB, run models.InboxWorkflowRun, index int, prepared preparedInboxIntegration, mappings map[string]string) (models.InboxIntegrationExecution, error) {
	var execution models.InboxIntegrationExecution
	if !prepared.Intent.Available || index < 0 || index >= 10 || run.ID == uuid.Nil || prepared.Definition.OrganizationID != run.OrganizationID || prepared.CaseID != run.CaseID || prepared.AccountID != run.AccountID || prepared.ContactID != run.ContactID || prepared.ActorID != run.ActorID {
		return execution, errInboxTransition
	}
	key, err := a.integrationEncryptionKey()
	if err != nil {
		return execution, err
	}
	raw, err := json.Marshal(integrationRequestSnapshot{mappings, prepared.Body})
	if err != nil {
		return execution, err
	}
	encrypted, err := crypto.Encrypt(string(raw), key)
	if err != nil {
		return execution, err
	}
	execution = models.InboxIntegrationExecution{BaseModel: models.BaseModel{ID: uuid.NewSHA1(run.ID, []byte("integration:"+strconv.Itoa(index)))}, OrganizationID: run.OrganizationID, RunID: run.ID, ActionIndex: index, IntegrationID: prepared.Definition.ID, CaseID: run.CaseID, AccountID: run.AccountID, ContactID: run.ContactID, ActorID: run.ActorID, DefinitionDigest: prepared.DefinitionDigest, RequestDigest: prepared.RequestDigest, CaseDigest: run.CaseDigest, EncryptedRequest: encrypted, State: "pending"}
	// Start conservatively at enqueue, no later than provider acceptance. This
	// deadline is immutable and never slides forward after a retry.
	execution.CreatedAt = time.Now().UTC()
	if prepared.Endpoint.IdempotencyHeader != "" && prepared.Endpoint.IdempotencyRetentionSeconds > 0 {
		until := execution.CreatedAt.Add(time.Duration(prepared.Endpoint.IdempotencyRetentionSeconds) * time.Second)
		execution.RetryUntil = &until
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&execution)
	if result.Error != nil {
		return execution, result.Error
	}
	if result.RowsAffected == 0 {
		var existing models.InboxIntegrationExecution
		if err := tx.First(&existing, "run_id=? AND action_index=?", run.ID, index).Error; err != nil {
			return execution, err
		}
		if existing.OrganizationID != execution.OrganizationID || existing.ActorID != execution.ActorID || existing.CaseID != execution.CaseID || existing.AccountID != execution.AccountID || existing.ContactID != execution.ContactID || existing.RequestDigest != execution.RequestDigest || existing.CaseDigest != execution.CaseDigest || existing.DefinitionDigest != execution.DefinitionDigest {
			return execution, errInboxConflict
		}
		execution = existing
	}
	return execution, nil
}

func (a *App) decodeIntegrationRequest(execution models.InboxIntegrationExecution) (integrationRequestSnapshot, error) {
	var snapshot integrationRequestSnapshot
	key, err := a.integrationEncryptionKey()
	if err != nil || !crypto.IsEncrypted(execution.EncryptedRequest) {
		return snapshot, errors.New("integration snapshot unavailable")
	}
	raw, err := crypto.Decrypt(execution.EncryptedRequest, key)
	if err != nil || len(raw) > 4*inboxintegration.MaxBody || json.Unmarshal([]byte(raw), &snapshot) != nil {
		return snapshot, errors.New("integration snapshot unavailable")
	}
	return snapshot, nil
}

// claimInboxIntegration atomically limits concurrent submissions to two across
// workers in this instance database. No network call holds this reservation tx.
func (a *App) claimInboxIntegration(ctx context.Context, id uuid.UUID) (models.InboxIntegrationExecution, preparedInboxIntegration, bool, error) {
	var execution models.InboxIntegrationExecution
	var prepared preparedInboxIntegration
	claimed := false
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-integration-dispatch").Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id=?", id).Error; err != nil {
			return err
		}
		if execution.State != "pending" {
			return nil
		}
		if execution.RetryRequested && (execution.RetryUntil == nil || !time.Now().UTC().Add(35*time.Second).Before(*execution.RetryUntil)) {
			return tx.Model(&execution).Updates(map[string]any{"state": "unknown", "outcome": "retry_window_expired", "retry_requested": false}).Error
		}
		var active int64
		if err := tx.Model(&models.InboxIntegrationExecution{}).Where("state='submitting'").Count(&active).Error; err != nil {
			return err
		}
		if active >= 2 {
			return nil
		}
		var run models.InboxWorkflowRun
		if err := tx.First(&run, "id=? AND organization_id=? AND case_id=? AND account_id=? AND contact_id=? AND actor_id=?", execution.RunID, execution.OrganizationID, execution.CaseID, execution.AccountID, execution.ContactID, execution.ActorID).Error; err != nil {
			return err
		}
		if run.State != inboxRunApplied && run.State != "integrating" {
			return blockIntegration(tx, &execution, "workflow_stopped")
		}
		if run.Sequenced && run.NextAction != execution.ActionIndex {
			return blockIntegration(tx, &execution, "action_order_changed")
		}
		var workflow models.InboxWorkflow
		if err := a.visibleInboxWorkflows(tx, run.OrganizationID, run.ActorID).First(&workflow, "id=?", run.WorkflowID).Error; err != nil {
			return blockIntegration(tx, &execution, "workflow_access_changed")
		}
		if !workflow.UpdatedAt.Equal(run.WorkflowUpdatedAt) || (workflow.Kind == "rule" && workflow.EnabledAt == nil) {
			return blockIntegration(tx, &execution, "workflow_changed")
		}
		if workflow.Kind == "rule" && !a.HasPermission(run.ActorID, models.ResourceSettingsGeneral, models.ActionWrite, run.OrganizationID) {
			return blockIntegration(tx, &execution, "permission_required")
		}
		if execution.ActionIndex < 0 || execution.ActionIndex >= len(workflow.Actions) || workflow.Actions[execution.ActionIndex].Type != "integration" || workflow.Actions[execution.ActionIndex].Value != execution.IntegrationID.String() {
			return blockIntegration(tx, &execution, "action_changed")
		}
		c, err := (inboxThread{run.OrganizationID, run.AccountID, run.ContactID}).latest(tx)
		if err != nil || c.ID != run.CaseID {
			return blockIntegration(tx, &execution, "case_changed")
		}
		digest, err := inboxSendCaseDigest(tx, c)
		if err != nil {
			return err
		}
		if execution.CaseDigest == "" || digest != execution.CaseDigest {
			return blockIntegration(tx, &execution, "case_changed")
		}
		snapshot, err := a.decodeIntegrationRequest(execution)
		if err != nil {
			return blockIntegration(tx, &execution, "snapshot_unavailable")
		}
		if workflowDigest(snapshot.Mappings) != workflowDigest(workflow.Actions[execution.ActionIndex].IntegrationInputs) {
			return blockIntegration(tx, &execution, "action_changed")
		}
		prepared, err = a.prepareInboxIntegration(tx, c, run.ActorID, execution.IntegrationID, snapshot.Mappings)
		if err != nil {
			return blockIntegration(tx, &execution, integrationProblemReason(err))
		}
		if !prepared.Intent.Available {
			if prepared.Intent.Reason == "dispatch_disabled" {
				return nil
			} // emergency stop holds pending work
			return blockIntegration(tx, &execution, prepared.Intent.Reason)
		}
		if prepared.DefinitionDigest != execution.DefinitionDigest || prepared.RequestDigest != execution.RequestDigest || workflowDigest(json.RawMessage(snapshot.Body)) != workflowDigest(json.RawMessage(prepared.Body)) {
			return blockIntegration(tx, &execution, "definition_or_values_changed")
		}
		now := time.Now().UTC()
		if err := tx.Model(&execution).Updates(map[string]any{"state": "submitting", "started_at": now, "attempt_count": gorm.Expr("attempt_count+1"), "retry_requested": false}).Error; err != nil {
			return err
		}
		execution.State, execution.StartedAt, execution.AttemptCount = "submitting", &now, execution.AttemptCount+1
		claimed = true
		return nil
	})
	return execution, prepared, claimed, err
}

func blockIntegration(tx *gorm.DB, execution *models.InboxIntegrationExecution, reason string) error {
	execution.State, execution.Outcome = "blocked", reason
	return tx.Model(execution).Updates(map[string]any{"state": "blocked", "outcome": reason}).Error
}

// dispatchInboxIntegration never automatically retries an external write. A
// durable submitting record precedes the call; only a signed explicit recovery
// can requeue an operation within its recorded idempotency guarantee.
func (a *App) dispatchInboxIntegration(ctx context.Context, id uuid.UUID) error {
	execution, prepared, claimed, err := a.claimInboxIntegration(ctx, id)
	if err != nil || !claimed {
		return err
	}
	return a.executeClaimedInboxIntegration(ctx, execution, prepared)
}

func (a *App) executeClaimedInboxIntegration(ctx context.Context, execution models.InboxIntegrationExecution, prepared preparedInboxIntegration) error {
	key, err := a.integrationEncryptionKey()
	if err != nil {
		return a.finishInboxIntegration(ctx, execution, "blocked", "encryption_unavailable", inboxintegration.Response{})
	}
	credential, err := crypto.Decrypt(prepared.Definition.Credential, key)
	if err != nil {
		return a.finishInboxIntegration(ctx, execution, "blocked", "credential_unavailable", inboxintegration.Response{})
	}
	executor := a.InboxIntegrationExecutor
	if executor == nil {
		executor = inboxintegration.NewClient()
	}
	if execution.AttemptCount > 1 {
		// A worker may pause after its database claim. Recheck immediately at
		// submission and bound the whole call inside the original guarantee.
		if execution.RetryUntil == nil || !time.Now().UTC().Add(35*time.Second).Before(*execution.RetryUntil) {
			return a.finishInboxIntegration(ctx, execution, "unknown", "retry_window_expired", inboxintegration.Response{})
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, execution.RetryUntil.Add(-5*time.Second))
		defer cancel()
	}
	response, err := executor.Execute(ctx, prepared.Endpoint, prepared.Definition.AuthMode, credential, prepared.Body, execution.ID.String())
	if err != nil {
		var failure inboxintegration.Failure
		if errors.As(err, &failure) && !failure.Ambiguous {
			return a.finishInboxIntegration(ctx, execution, "failed", safeIntegrationFailure(failure.Category), response)
		}
		return a.finishInboxIntegration(ctx, execution, "unknown", "needs_reconciliation", response)
	}
	// Validate selected outputs before persisting anything the provider returned.
	selected := map[string]json.RawMessage{}
	fields, err := a.integrationOutputFields(a.DB, prepared.Definition, execution.ActorID)
	if err != nil {
		return a.finishInboxIntegration(ctx, execution, "unknown", "output_access_changed", response)
	}
	for _, output := range prepared.Definition.Outputs {
		value, ok := response.Fields[output.Name]
		if !ok || fields[output.DefinitionID].ValidateValue(value) != nil || emptyInboxField(value) {
			return a.finishInboxIntegration(ctx, execution, "unknown", "output_invalid", response)
		}
		if inboxIntegrationResultContainsCredential(value, credential) {
			return a.finishInboxIntegration(ctx, execution, "unknown", "output_invalid", response)
		}
		selected[output.Name] = value
	}
	if prepared.Definition.ReferenceKey != "" {
		value, ok := response.Fields[prepared.Definition.ReferenceKey]
		var reference string
		if !ok || json.Unmarshal(value, &reference) != nil || strings.TrimSpace(reference) == "" || len(reference) > 200 || strings.ContainsAny(reference, "\r\n\x00") || (credential != "" && strings.Contains(reference, credential)) {
			return a.finishInboxIntegration(ctx, execution, "unknown", "output_invalid", response)
		}
		selected[prepared.Definition.ReferenceKey] = value
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return err
	}
	execution.EncryptedResult, err = crypto.Encrypt(string(raw), key)
	if err != nil {
		return err
	}
	return a.finishInboxIntegration(ctx, execution, "succeeded", "", response)
}

func inboxIntegrationResultContainsCredential(value json.RawMessage, credential string) bool {
	if credential == "" {
		return false
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return strings.Contains(text, credential)
	}
	// Scalar numeric/boolean values must not bypass redaction merely because
	// they are represented without JSON quotes.
	return strings.Contains(string(value), credential)
}

func safeIntegrationFailure(category string) string {
	switch category {
	case "endpoint_invalid", "destination_blocked", "method_invalid", "idempotency_invalid", "request_invalid", "credential_invalid", "dns_unavailable", "submission_uncertain", "provider_rejected", "response_invalid":
		return category
	default:
		return "provider_failure"
	}
}

func (a *App) finishInboxIntegration(ctx context.Context, execution models.InboxIntegrationExecution, state, outcome string, response inboxintegration.Response) error {
	// Use a fresh bounded finalisation context if the provider exhausted the
	// caller deadline. If persistence still fails, stale submitting is never replayed.
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored models.InboxIntegrationExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stored, "id=? AND organization_id=?", execution.ID, execution.OrganizationID).Error; err != nil {
			return err
		}
		if stored.State != "submitting" {
			return nil
		}
		now := time.Now().UTC()
		updates := map[string]any{"state": state, "outcome": outcome, "encrypted_result": execution.EncryptedResult, "provider_status": response.Status, "duration_millis": response.Duration.Milliseconds(), "completed_at": now}
		if state == "succeeded" {
			applied, err := a.applyInboxIntegrationOutputs(tx, execution, now)
			if err != nil {
				return err
			}
			if applied {
				updates["outputs_applied_at"] = now
			} else {
				updates["outcome"] = "output_context_changed"
			}
		}
		return tx.Model(&stored).Updates(updates).Error
	})
}

func (a *App) reconcileInterruptedInboxIntegrations(ctx context.Context, now time.Time) error {
	// No outbound call: surviving submissions are held, not inferred failed.
	return a.DB.WithContext(ctx).Model(&models.InboxIntegrationExecution{}).Where("state='submitting' AND started_at<?", now.Add(-2*time.Minute)).Updates(map[string]any{"state": "unknown", "outcome": "needs_reconciliation"}).Error
}
