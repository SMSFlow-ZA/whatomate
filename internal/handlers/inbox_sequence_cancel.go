package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxSequenceCancelPreview struct {
	Run          models.InboxWorkflowRun
	Case         models.InboxCase
	Executions   []models.InboxIntegrationExecution
	Confirmation string
	Reason       string
	Unresolved   bool
	InFlight     bool
}

func (a *App) inboxRecoveryRun(db *gorm.DB, org, actor, id uuid.UUID) (models.InboxWorkflowRun, error) {
	var run models.InboxWorkflowRun
	cases := a.scopeInboxCaseHistory(db.Session(&gorm.Session{NewDB: true}).Model(&models.InboxCase{}), actor, org).Select("inbox_cases.id")
	q := db.Where("organization_id=? AND case_id IN (?)", org, cases)
	if !a.HasPermission(actor, models.ResourceSettingsGeneral, models.ActionWrite, org) {
		q = q.Where("kind='macro' AND (actor_id=? OR initiated_by_id=?)", actor, actor)
	}
	err := q.First(&run, "id=?", id).Error
	return run, err
}

func (a *App) previewInboxSequenceCancellation(db *gorm.DB, org, actor, id uuid.UUID) (inboxSequenceCancelPreview, error) {
	var out inboxSequenceCancelPreview
	var err error
	out.Run, err = a.inboxRecoveryRun(db, org, actor, id)
	if err != nil {
		return out, err
	}
	run := out.Run
	if !run.Sequenced {
		out.Reason = "not_a_sequenced_workflow"
		return out, nil
	}
	if run.State == inboxSequenceCancelled || run.State == inboxRunSent || run.State == inboxRunApplied {
		out.Reason = "workflow_already_stopped"
		return out, nil
	}
	if run.State == inboxSequenceSending {
		out.Reason = "message_submission_in_progress"
		return out, nil
	}
	if run.State == inboxSequenceUnknown && run.NextAction >= 0 && run.NextAction < len(run.ActionOutcomes) {
		kind := run.ActionOutcomes[run.NextAction].Type
		if kind == "send_text" || kind == "send_template" {
			out.Reason = "reconcile_message_submission_first"
			return out, nil
		}
	}
	if err := a.scopeInboxCaseHistory(db.Model(&models.InboxCase{}), actor, org).First(&out.Case, "inbox_cases.id=?", run.CaseID).Error; err != nil {
		return out, err
	}
	out.Executions = []models.InboxIntegrationExecution{}
	if err := db.Where("run_id=? AND organization_id=?", id, org).Order("action_index,id").Find(&out.Executions).Error; err != nil {
		return out, err
	}
	states := []any{}
	for _, e := range out.Executions {
		out.Unresolved = out.Unresolved || e.State == "unknown" || e.State == "submitting" || e.State == "pending" && e.AttemptCount > 0
		out.InFlight = out.InFlight || e.State == "submitting"
		states = append(states, []any{e.ID, e.UpdatedAt, e.State, e.Outcome, e.AttemptCount})
	}
	out.Confirmation = inboxPreviewConfirmation([]any{"sequence-cancel", actor, run.ID, run.UpdatedAt, run.State, run.NextAction, run.ActorID, run.WorkflowUpdatedAt, run.CaseID, run.AccountID, states})
	return out, nil
}

func (a *App) PreviewInboxSequenceCancellation(r *fastglue.Request) error {
	org, actor, ok := a.integrationOperatorAuth(r, true)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "run_id", "Workflow run")
	if err != nil {
		return nil
	}
	out, err := a.previewInboxSequenceCancellation(a.DB, org, actor, id)
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"run_id": id, "case_id": out.Run.CaseID, "account_id": out.Run.AccountID, "eligible": out.Confirmation != "", "confirmation": out.Confirmation, "reason": out.Reason, "provider_outcome_unresolved": out.Unresolved, "submission_in_progress": out.InFlight, "does_not_retract_submitted_operations": true})
}

func (a *App) CancelInboxSequence(r *fastglue.Request) error {
	org, actor, ok := a.integrationOperatorAuth(r, true)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "run_id", "Workflow run")
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
	if input.OperationID == uuid.Nil || len(input.Confirmation) != 64 || !safeInboxRecoveryText(input.Reason, 1000, "", inboxintegration.Endpoint{}) {
		return a.inboxError(r, errInboxTransition)
	}
	digest := workflowDigest([]any{"cancel_remaining", org, actor, id, input.Reason, input.Confirmation})
	var decision models.InboxIntegrationRecovery
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-integration-recovery:"+input.OperationID.String()).Error; err != nil {
			return err
		}
		run, err := a.inboxRecoveryRun(tx, org, actor, id)
		if err != nil {
			return err
		}
		err = tx.First(&decision, "id=?", input.OperationID).Error
		if err == nil {
			if decision.OrganizationID != org || decision.RunID == nil || *decision.RunID != id || decision.ActorID != actor || decision.RequestDigest != digest {
				return errInboxConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var organization models.Organization
		if err := tx.Clauses(clause.Locking{Strength: "SHARE", Options: "NOWAIT"}).Select("id").First(&organization, "id=?", org).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&run, "id=? AND organization_id=?", id, org).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		var executions []models.InboxIntegrationExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("run_id=? AND organization_id=?", id, org).Order("action_index,id").Find(&executions).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		out, err := a.previewInboxSequenceCancellation(tx, org, actor, id)
		if err != nil {
			return err
		}
		if out.Confirmation == "" || !inboxPreviewMatches(out.Confirmation, input.Confirmation) {
			return errInboxConflict
		}
		// Stopping must still work if the instance key is unavailable. Check
		// known current credentials when readable, without decrypting results.
		for _, e := range executions {
			var d models.InboxIntegration
			if err := tx.First(&d, "id=? AND organization_id=?", e.IntegrationID, org).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			endpoint, _ := a.integrationEndpoint(d.EndpointKey, org)
			credential := ""
			if key, err := a.integrationEncryptionKey(); err == nil {
				credential, _ = crypto.Decrypt(d.Credential, key)
			}
			if !safeInboxRecoveryText(input.Reason, 1000, credential, endpoint) {
				return errInboxTransition
			}
		}
		c := inboxIntegrationRecoveryContext{Run: run, Case: out.Case}
		c.Execution.State, c.Execution.Outcome = run.State, run.Outcome
		decision = newInboxIntegrationRecovery(input.OperationID, c, actor, "cancel_remaining", input.Reason, digest)
		for i := range executions {
			e := &executions[i]
			if e.State == "pending" || e.State == "blocked" && e.AttemptCount == 0 {
				state, outcome := "cancelled", "workflow_cancelled_before_submission"
				if e.AttemptCount > 0 {
					state, outcome = "unknown", "retry_cancelled_outcome_unresolved"
				}
				updates := map[string]any{"state": state, "outcome": outcome, "retry_requested": false}
				if state == "cancelled" {
					updates["completed_at"] = time.Now().UTC()
				}
				if err := tx.Model(e).Updates(updates).Error; err != nil {
					return err
				}
				e.State, e.Outcome = state, outcome
			}
		}
		for i := run.NextAction; i >= 0 && i < len(run.ActionOutcomes); i++ {
			run.ActionOutcomes[i].Status, run.ActionOutcomes[i].Reason = "cancelled", "remaining_work_cancelled"
			for _, e := range executions {
				if e.ActionIndex == i && (e.State == "submitting" || e.State == "unknown" || e.State == "succeeded") {
					run.ActionOutcomes[i].Status, run.ActionOutcomes[i].Reason = e.State, "provider_record_retained_after_cancellation"
				}
			}
		}
		run.State, run.Outcome = inboxSequenceCancelled, "cancelled_by_operator"
		if out.Unresolved {
			run.Outcome = "cancelled_provider_review_required"
		}
		if err := saveInboxSequence(tx, &run); err != nil {
			return err
		}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		return recordInboxIntegrationRecoveryActivity(tx, out.Case, actor, "workflow_sequence_cancelled", input.Reason)
	})
	if err != nil {
		return a.inboxError(r, inboxIntegrationPersistenceError(err))
	}
	return r.SendEnvelope(map[string]any{"recovery": decision, "cancelled": true, "does_not_retract_submitted_operations": true})
}

func (a *App) ListInboxRunRecoveries(r *fastglue.Request) error {
	org, actor, ok := a.integrationOperatorAuth(r, true)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "run_id", "Workflow run")
	if err != nil {
		return nil
	}
	if _, err := a.inboxRecoveryRun(a.DB, org, actor, id); err != nil {
		return a.inboxError(r, err)
	}
	legacy := a.DB.Model(&models.InboxIntegrationExecution{}).Select("id").Where("run_id=? AND organization_id=?", id, org)
	q, err := integrationHistoryPage(r, a.DB.Where("organization_id=? AND (run_id=? OR execution_id IN (?))", org, id, legacy))
	if err != nil {
		return a.inboxError(r, err)
	}
	items := []models.InboxIntegrationRecovery{}
	if err := q.Find(&items).Error; err != nil {
		return a.inboxError(r, err)
	}
	more := len(items) > 50
	next := ""
	if more {
		items = items[:50]
		next = integrationHistoryCursor(items[49].CreatedAt, items[49].ID)
	}
	for i := range items {
		items[i].HasEvidence = items[i].EncryptedEvidence != ""
	}
	return r.SendEnvelope(map[string]any{"recoveries": items, "has_more": more, "next_cursor": next})
}
