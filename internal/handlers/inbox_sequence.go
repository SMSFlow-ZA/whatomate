package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	inboxSequenceRunning   = "integrating"
	inboxSequenceSending   = "sequence_sending"
	inboxSequenceReview    = "sequence_review"
	inboxSequenceUnknown   = "sequence_unknown"
	inboxSequenceBlocked   = "sequence_blocked"
	inboxSequenceCancelled = "sequence_cancelled"
)

var errInboxSequencePending = errors.New("ordered workflow is pending")

type inboxIntegrationApproval struct {
	Definition string `json:"definition"`
	Request    string `json:"request"`
}
type inboxSequencePlan struct {
	Integrations    map[int]inboxIntegrationApproval `json:"integrations"`
	ReplyDigest     string                           `json:"reply_digest"`
	SendSkipReason  string                           `json:"send_skip_reason,omitempty"`
	DeferredActions map[int]bool                     `json:"deferred_actions,omitempty"`
}

func inboxWorkflowHasIntegration(w models.InboxWorkflow) bool {
	for _, action := range w.Actions {
		if action.Type == "integration" {
			return true
		}
	}
	return false
}

func inboxSequenceApprovals(integrations map[int]preparedInboxIntegration) map[int]inboxIntegrationApproval {
	result := map[int]inboxIntegrationApproval{}
	for index, prepared := range integrations {
		result[index] = inboxIntegrationApproval{prepared.DefinitionDigest, prepared.RequestDigest}
	}
	return result
}

func inboxIntegrationIntents(integrations map[int]preparedInboxIntegration) map[int]inboxIntegrationIntent {
	result := map[int]inboxIntegrationIntent{}
	for index, prepared := range integrations {
		result[index] = prepared.Intent
	}
	return result
}

func (a *App) encryptInboxSequencePlan(plan inboxSequencePlan) (string, error) {
	key, err := a.integrationEncryptionKey()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return crypto.Encrypt(string(raw), key)
}

func (a *App) inboxSequencePlan(run models.InboxWorkflowRun) (inboxSequencePlan, error) {
	var plan inboxSequencePlan
	key, err := a.integrationEncryptionKey()
	if err != nil || !crypto.IsEncrypted(run.EncryptedSequencePlan) {
		return plan, errInboxTransition
	}
	raw, err := crypto.Decrypt(run.EncryptedSequencePlan, key)
	if err != nil || len(raw) > 16384 || json.Unmarshal([]byte(raw), &plan) != nil {
		return plan, errInboxTransition
	}
	return plan, nil
}

func (a *App) queueInboxSequence(tx *gorm.DB, c models.InboxCase, run *models.InboxWorkflowRun, preview inboxWorkflowPreview, sendReserved bool, skipReason string) error {
	if !preview.Matches || !preview.ActionsEligible {
		return errInboxTransition
	}
	plan := inboxSequencePlan{Integrations: inboxSequenceApprovals(preview.Integrations), ReplyDigest: preview.Digest, SendSkipReason: skipReason, DeferredActions: inboxSequenceDeferredActions(preview)}
	encrypted, err := a.encryptInboxSequencePlan(plan)
	if err != nil {
		return err
	}
	run.Sequenced, run.NextAction, run.State, run.SequenceSendReserved, run.EncryptedSequencePlan = true, 0, inboxSequenceRunning, sendReserved, encrypted
	initiator := run.ActorID
	run.InitiatedByID = &initiator
	run.ActionOutcomes, run.ConditionOutcomes, run.ConditionsMatched = preview.ActionOutcomes, preview.ConditionOutcomes, &preview.Matches
	run.SendDigest = preview.Digest
	run.CaseDigest, err = inboxSendCaseDigest(tx, c)
	if err != nil {
		return err
	}
	return saveInboxWorkflowRun(tx, c, run)
}

func saveInboxSequence(tx *gorm.DB, run *models.InboxWorkflowRun) error {
	raw, err := json.Marshal(run.ActionOutcomes)
	if err != nil {
		return err
	}
	return tx.Model(run).Updates(map[string]any{"state": run.State, "outcome": run.Outcome, "next_action": run.NextAction, "case_digest": run.CaseDigest, "send_digest": run.SendDigest, "submission_started_at": run.SubmissionStartedAt, "action_outcomes": gorm.Expr("?::jsonb", string(raw))}).Error
}

func stopInboxSequence(tx *gorm.DB, run *models.InboxWorkflowRun, state, reason string) error {
	run.State, run.Outcome = state, reason
	if run.NextAction >= 0 && run.NextAction < len(run.ActionOutcomes) {
		run.ActionOutcomes[run.NextAction].Status = state
		run.ActionOutcomes[run.NextAction].Reason = reason
	}
	return saveInboxSequence(tx, run)
}

type inboxSequenceDispatch struct {
	IntegrationID uuid.UUID
	Send          *models.InboxWorkflowRun
}

// Advance one step under database locks. External work starts only after this
// transaction returns. The cursor and local action commit together.
func (a *App) advanceInboxSequence(ctx context.Context, id, org uuid.UUID) (inboxSequenceDispatch, error) {
	var dispatch inboxSequenceDispatch
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Organisation first: integration result application locks it before
		// updating the run too. Do not hold a run lock while waiting for it.
		var organization models.Organization
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id,settings,inbox_workflows_enabled,inbox_automation_sends_enabled").First(&organization, "id=?", org).Error; err != nil {
			return err
		}
		var run models.InboxWorkflowRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "id=? AND organization_id=? AND sequenced", id, org).Error; err != nil {
			return err
		}
		if run.State != inboxSequenceRunning {
			return nil
		}
		enabled, _ := organization.Settings["inbox_integrations_enabled"].(bool)
		if !organization.InboxWorkflowsEnabled || !enabled || a.Config == nil || !a.Config.InboxAutomation.IntegrationDispatchEnabled {
			return nil
		}
		var w models.InboxWorkflow
		if err := a.visibleInboxWorkflows(tx, org, run.ActorID).First(&w, "id=?", run.WorkflowID).Error; err != nil {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "workflow_access_changed")
		}
		if !w.UpdatedAt.Equal(run.WorkflowUpdatedAt) || (w.Kind == "rule" && w.EnabledAt == nil) {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "workflow_changed")
		}
		if !a.workflowActorActive(tx, org, run.ActorID) || !a.HasPermission(run.ActorID, models.ResourceChat, models.ActionWrite, org) || (w.Kind == "rule" && !a.HasPermission(run.ActorID, models.ResourceSettingsGeneral, models.ActionWrite, org)) {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "permission_required")
		}
		if run.NextAction < 0 || run.NextAction > len(w.Actions) || len(run.ActionOutcomes) != len(w.Actions) {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "workflow_plan_invalid")
		}
		if run.NextAction == len(w.Actions) {
			run.State, run.Outcome = inboxRunApplied, ""
			for _, item := range run.ActionOutcomes {
				if item.Status == inboxRunSent {
					run.State = inboxRunSent
				}
			}
			return saveInboxSequence(tx, &run)
		}
		plan, err := a.inboxSequencePlan(run)
		if err != nil {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "snapshot_unavailable")
		}
		thread := inboxThread{org, run.AccountID, run.ContactID}
		if err := thread.lock(tx); err != nil {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "conversation_unavailable")
		}
		c, err := thread.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil || c.ID != run.CaseID {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "case_changed")
		}
		var scoped models.InboxCase
		if err := a.scopeInboxCases(tx.Model(&scoped), run.ActorID, org).First(&scoped, "inbox_cases.id=?", c.ID).Error; err != nil {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "case_access_changed")
		}
		// A successful integration updates the parent's digest atomically. A
		// separate user/event edit instead requires an explicit fresh review.
		digest, err := inboxSendCaseDigest(tx, c)
		if err != nil {
			return err
		}
		action := w.Actions[run.NextAction]
		if action.Type == "integration" {
			var execution models.InboxIntegrationExecution
			err := tx.First(&execution, "run_id=? AND action_index=? AND organization_id=?", run.ID, run.NextAction, org).Error
			if err == nil {
				switch execution.State {
				case "succeeded":
					if execution.OutputsAppliedAt == nil && execution.OutputsDiscardedAt == nil {
						return stopInboxSequence(tx, &run, inboxSequenceReview, "output_context_changed")
					}
					run.ActionOutcomes[run.NextAction].Status, run.ActionOutcomes[run.NextAction].Reason = "succeeded", "integration_completed"
					if execution.OutputsDiscardedAt != nil {
						run.ActionOutcomes[run.NextAction].Reason = "integration_outputs_discarded"
					}
					run.NextAction++
					return saveInboxSequence(tx, &run)
				case "pending":
					dispatch.IntegrationID = execution.ID
					return nil
				case "submitting":
					return nil
				case "unknown":
					return stopInboxSequence(tx, &run, inboxSequenceUnknown, "needs_reconciliation")
				default:
					return stopInboxSequence(tx, &run, inboxSequenceBlocked, execution.Outcome)
				}
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if digest != run.CaseDigest {
			return stopInboxSequence(tx, &run, inboxSequenceReview, "case_changed")
		}
		if plan.DeferredActions[run.NextAction] && (action.Type == "integration" || run.SequenceSendReserved) {
			return stopInboxSequence(tx, &run, inboxSequenceReview, "prior_integration_result_requires_review")
		}
		account, contact, err := a.inboxWorkflowContext(tx, c)
		if err != nil {
			return err
		}
		if action.Type == "integration" {
			definitionID, err := uuid.Parse(action.Value)
			if err != nil {
				return stopInboxSequence(tx, &run, inboxSequenceBlocked, "action_changed")
			}
			prepared, err := a.prepareInboxIntegration(tx, c, run.ActorID, definitionID, action.IntegrationInputs)
			if err != nil {
				return stopInboxSequence(tx, &run, inboxSequenceBlocked, integrationProblemReason(err))
			}
			if !prepared.Intent.Available {
				return stopInboxSequence(tx, &run, inboxSequenceBlocked, prepared.Intent.Reason)
			}
			approved, ok := plan.Integrations[run.NextAction]
			if !ok || approved.Definition != prepared.DefinitionDigest || approved.Request != prepared.RequestDigest {
				return stopInboxSequence(tx, &run, inboxSequenceReview, "integration_review_required")
			}
			execution, err := a.enqueueInboxIntegration(tx, run, run.NextAction, prepared, action.IntegrationInputs)
			if err != nil {
				return err
			}
			dispatch.IntegrationID = execution.ID
			return nil
		}
		if action.Type == "send_text" || action.Type == "send_template" {
			if !run.SequenceSendReserved {
				run.ActionOutcomes[run.NextAction].Status, run.ActionOutcomes[run.NextAction].Reason = "not_applied", plan.SendSkipReason
				run.NextAction++
				return saveInboxSequence(tx, &run)
			}
			if !a.inboxDispatchAllowed() || (w.Kind == "rule" && !organization.InboxAutomationSendsEnabled) {
				return nil
			}
			reply, err := a.previewInboxReplyForActor(tx, c, account, contact, action, time.Now().UTC(), run.ActorID)
			if err != nil {
				return stopInboxSequence(tx, &run, inboxSequenceBlocked, inboxEligibilityReason(err))
			}
			if inboxReplyFingerprint(c, account, contact, reply) != plan.ReplyDigest {
				return stopInboxSequence(tx, &run, inboxSequenceReview, "reply_review_required")
			}
			now := time.Now().UTC()
			run.State, run.SubmissionStartedAt, run.SendDigest = inboxSequenceSending, &now, plan.ReplyDigest
			run.ActionOutcomes[run.NextAction].Status = inboxRunSubmitting
			if err := saveInboxSequence(tx, &run); err != nil {
				return err
			}
			dispatch.Send = &run
			return nil
		}
		one := w
		one.Actions = []models.InboxAction{action}
		outcomes, eligible, err := a.preflightInboxActions(tx, one, c, run.ActorID, time.Now().UTC())
		if err != nil {
			return err
		}
		if !eligible {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, outcomes[0].Reason)
		}
		outcomes[0].Index = run.NextAction
		if err := a.runInboxWorkflowActions(tx, &c, &contact, run.ActorID, one, time.Now().UTC(), &outcomes); err != nil {
			return err
		}
		run.ActionOutcomes[run.NextAction] = outcomes[0]
		run.NextAction++
		run.CaseDigest, err = inboxSendCaseDigest(tx, c)
		if err != nil {
			return err
		}
		return saveInboxSequence(tx, &run)
	})
	return dispatch, err
}

func (a *App) finishInboxSequenceSend(ctx context.Context, run models.InboxWorkflowRun, state, reason string) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored models.InboxWorkflowRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stored, "id=? AND organization_id=?", run.ID, run.OrganizationID).Error; err != nil {
			return err
		}
		if stored.State != inboxSequenceSending || stored.NextAction != run.NextAction {
			return nil
		}
		switch state {
		case inboxRunSent:
			stored.ActionOutcomes[stored.NextAction].Status, stored.ActionOutcomes[stored.NextAction].Reason = inboxRunSent, reason
			stored.NextAction++
			stored.State, stored.Outcome = inboxSequenceRunning, ""
			// Human-reply recording updates this digest atomically for its own
			// change only. Do not absorb unrelated edits made during the call.
			return saveInboxSequence(tx, &stored)
		case inboxRunUnknown:
			return stopInboxSequence(tx, &stored, inboxSequenceUnknown, reason)
		default:
			return stopInboxSequence(tx, &stored, inboxSequenceBlocked, reason)
		}
	})
}

func (a *App) runInboxSequences(ctx context.Context) error {
	if err := a.reconcileInterruptedInboxIntegrations(ctx, time.Now().UTC()); err != nil {
		return err
	}
	var stale []models.InboxWorkflowRun
	if err := a.DB.WithContext(ctx).Where("sequenced AND (state=? AND submission_started_at<? OR state=?)", inboxSequenceSending, time.Now().UTC().Add(-2*time.Minute), inboxSequenceUnknown).
		Where("(action_outcomes->CAST(next_action AS integer))->>'type' IN ('send_text','send_template')").Order("updated_at,id").Limit(20).Find(&stale).Error; err != nil {
		return err
	}
	for _, run := range stale {
		if run.NextAction < 0 || run.NextAction >= len(run.ActionOutcomes) {
			continue
		}
		if run.ActionOutcomes[run.NextAction].Type != "send_text" && run.ActionOutcomes[run.NextAction].Type != "send_template" {
			continue
		}
		if run.State == inboxSequenceUnknown && !a.inboxSendReceipt(run) {
			continue
		}
		state, reason := inboxRunUnknown, "needs_reconciliation"
		if a.inboxSendReceipt(run) {
			state, reason = inboxRunSent, "reconciled"
		}
		if run.State == inboxSequenceUnknown {
			if err := a.DB.Model(&run).Where("state=?", inboxSequenceUnknown).Update("state", inboxSequenceSending).Error; err != nil {
				return err
			}
		}
		if err := a.finishInboxSequenceSend(ctx, run, state, reason); err != nil {
			return err
		}
	}
	if a.Config == nil || !a.Config.InboxAutomation.IntegrationDispatchEnabled {
		return nil
	}
	var runs []models.InboxWorkflowRun
	if err := a.DB.WithContext(ctx).Select("id,organization_id").Where("sequenced AND state=?", inboxSequenceRunning).
		Where("EXISTS (SELECT 1 FROM organizations o WHERE o.id=inbox_workflow_runs.organization_id AND o.deleted_at IS NULL AND o.inbox_workflows_enabled AND o.settings->>'inbox_integrations_enabled'='true')").
		Where("NOT sequence_send_reserved OR COALESCE((action_outcomes->CAST(next_action AS integer))->>'type','') NOT IN ('send_text','send_template') OR (? AND (kind<>'rule' OR EXISTS (SELECT 1 FROM organizations o WHERE o.id=inbox_workflow_runs.organization_id AND o.inbox_automation_sends_enabled)))", a.inboxDispatchAllowed()).
		Where("NOT EXISTS (SELECT 1 FROM inbox_integration_executions e WHERE e.run_id=inbox_workflow_runs.id AND e.action_index=inbox_workflow_runs.next_action AND e.state='submitting' AND e.deleted_at IS NULL)").Order("updated_at,id").Limit(20).Find(&runs).Error; err != nil {
		return err
	}
	jobs := []inboxSequenceDispatch{}
	for _, run := range runs {
		dispatch, err := a.advanceInboxSequence(ctx, run.ID, run.OrganizationID)
		if err != nil {
			return err
		}
		if dispatch.IntegrationID != uuid.Nil || dispatch.Send != nil {
			jobs = append(jobs, dispatch)
		}
		if len(jobs) >= 2 {
			break
		}
	}
	var wg sync.WaitGroup
	failures := make(chan error, len(jobs))
	for _, job := range jobs {
		wg.Add(1)
		go func(job inboxSequenceDispatch) {
			defer wg.Done()
			if job.IntegrationID != uuid.Nil {
				failures <- a.dispatchInboxIntegration(ctx, job.IntegrationID)
				return
			}
			state, reason := a.dispatchClaimedInboxSend(ctx, *job.Send)
			failures <- a.finishInboxSequenceSend(ctx, *job.Send, state, reason)
		}(job)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			return err
		}
	}
	return nil
}
