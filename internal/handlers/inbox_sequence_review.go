package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxSequenceReviewSnapshot struct {
	Run          models.InboxWorkflowRun
	Case         models.InboxCase
	Workflow     models.InboxWorkflow
	Preview      inboxWorkflowPreview
	Confirmation string
}

func (a *App) reviewInboxSequence(db *gorm.DB, org, actor, id uuid.UUID) (inboxSequenceReviewSnapshot, error) {
	var result inboxSequenceReviewSnapshot
	if err := db.First(&result.Run, "id=? AND organization_id=? AND sequenced", id, org).Error; err != nil {
		return result, err
	}
	run := result.Run
	if run.ActorID != actor && !a.HasPermission(actor, models.ResourceSettingsGeneral, models.ActionWrite, org) {
		return result, gorm.ErrRecordNotFound
	}
	if run.State != inboxSequenceReview && run.State != inboxSequenceBlocked {
		return result, errInboxConflict
	}
	if err := a.visibleInboxWorkflows(db, org, actor).First(&result.Workflow, "id=?", run.WorkflowID).Error; err != nil {
		return result, err
	}
	w := result.Workflow
	if !w.UpdatedAt.Equal(run.WorkflowUpdatedAt) || run.NextAction < 0 || run.NextAction >= len(w.Actions) || len(run.ActionOutcomes) != len(w.Actions) || (w.Kind == "rule" && !a.HasPermission(actor, models.ResourceSettingsGeneral, models.ActionWrite, org)) {
		return result, errInboxConflict
	}
	c, err := (inboxThread{org, run.AccountID, run.ContactID}).latest(db)
	if err != nil || c.ID != run.CaseID {
		return result, errInboxConflict
	}
	if err := a.scopeInboxCases(db.Model(&c), actor, org).First(&c, "inbox_cases.id=?", run.CaseID).Error; err != nil {
		return result, err
	}
	// A posted or uncertain external operation requires reconciliation first;
	// a context review must never implicitly retry it.
	if w.Actions[run.NextAction].Type == "integration" {
		var execution models.InboxIntegrationExecution
		err := db.First(&execution, "run_id=? AND action_index=? AND organization_id=?", run.ID, run.NextAction, org).Error
		if err == nil && (execution.AttemptCount != 0 || (execution.State != "pending" && execution.State != "blocked")) {
			return result, errInboxConflict
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return result, err
		}
	}
	account, contact, err := a.inboxWorkflowContext(db, c)
	if err != nil {
		return result, err
	}
	remaining := w
	remaining.Actions = append([]models.InboxAction{}, w.Actions[run.NextAction:]...)
	remaining.Conditions = models.InboxQueueFilter{}
	preview, err := a.evaluateInboxWorkflow(db, remaining, c, account, contact, actor, time.Now().UTC())
	if err != nil {
		return result, err
	}
	result.Case, result.Preview = c, preview
	result.Confirmation = inboxPreviewConfirmation([]any{"sequence-review", run.ID, run.NextAction, run.ActorID, actor, run.State, run.Outcome, preview.Confirmation})
	return result, nil
}

func (a *App) PreviewInboxSequence(r *fastglue.Request) error {
	org, actor, ok := a.inboxFeatureAuth(r, models.ActionWrite)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "run_id", "Workflow run")
	if err != nil {
		return nil
	}
	snapshot, err := a.reviewInboxSequence(a.DB, org, actor, id)
	if err != nil {
		return a.inboxError(r, err)
	}
	account, contact, err := a.inboxWorkflowContext(a.DB, snapshot.Case)
	if err != nil {
		return a.inboxError(r, err)
	}
	name, phone := contact.ProfileName, contact.PhoneNumber
	mask, err := inboxMessageMasking(a.DB, org)
	if err != nil {
		return a.inboxError(r, err)
	}
	if mask {
		name, phone = utils.MaskIfPhoneNumber(name), utils.MaskPhoneNumber(phone)
	}
	return r.SendEnvelope(map[string]any{"run_id": id, "next_action": snapshot.Run.NextAction, "case_id": snapshot.Case.ID, "case_version": snapshot.Case.Version, "actions": snapshot.Workflow.Actions[snapshot.Run.NextAction:], "condition_outcomes": snapshot.Preview.ConditionOutcomes, "action_outcomes": snapshot.Preview.ActionOutcomes, "integrations": inboxIntegrationIntents(snapshot.Preview.Integrations), "deferred_actions": snapshot.Preview.DeferredActions, "confirmation": snapshot.Confirmation, "matches": snapshot.Preview.Matches, "actions_eligible": snapshot.Preview.ActionsEligible, "send_unavailable": snapshot.Preview.SendUnavailable, "send_unavailable_reason": snapshot.Preview.SendUnavailableReason, "send_will_be_skipped": !snapshot.Run.SequenceSendReserved, "recipient": map[string]string{"name": name, "phone": phone}, "account": account.Name, "reply": snapshot.Preview.Reply})
}

func (a *App) ResumeInboxSequence(r *fastglue.Request) error {
	org, actor, ok := a.inboxFeatureAuth(r, models.ActionWrite)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "run_id", "Workflow run")
	if err != nil {
		return nil
	}
	var input struct {
		OperationID  *uuid.UUID `json:"operation_id"`
		Confirmation string     `json:"confirmation"`
		Reason       string     `json:"reason"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Confirmation) != 64 || input.Reason == "" || utf8.RuneCountInString(input.Reason) > 1000 {
		return a.inboxError(r, errInboxTransition)
	}
	operation := uuid.Nil
	if input.OperationID != nil {
		operation = *input.OperationID
		if operation == uuid.Nil || strings.ContainsAny(input.Reason, "\x00\r\n") {
			return a.inboxError(r, errInboxTransition)
		}
	}
	digest := workflowDigest([]any{"resume_sequence", org, actor, id, input.Confirmation, input.Reason})
	var resumed models.InboxWorkflowRun
	var decision models.InboxIntegrationRecovery
	// A disconnected caller must be able to recover an accepted decision by
	// UUID. Legacy callers retain their fresh-review behaviour without one.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if operation != uuid.Nil {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-integration-recovery:"+operation.String()).Error; err != nil {
				return err
			}
			var err error
			resumed, err = a.inboxRecoveryRun(tx, org, actor, id)
			if err != nil {
				return err
			}
			if !resumed.Sequenced {
				return gorm.ErrRecordNotFound
			}
			err = tx.First(&decision, "id=?", operation).Error
			if err == nil {
				if decision.OrganizationID != org || decision.ActorID != actor || decision.RunID == nil || *decision.RunID != id || decision.Kind != "resume_sequence" || decision.RequestDigest != digest {
					return errInboxConflict
				}
				return nil // Receipt only: do not advance or dispatch again.
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		enabled, err := inboxWorkflowEnabled(tx, org, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxDisabled
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&resumed, "id=? AND organization_id=? AND sequenced", id, org).Error; err != nil {
			return err
		}
		// A caller can race the worker even with a stale review open. Reject a
		// submitting/completed run before touching the contact lock: human reply
		// recording takes that lock before updating this run's expected digest.
		if resumed.State != inboxSequenceReview && resumed.State != inboxSequenceBlocked {
			return errInboxConflict
		}
		if err := (inboxThread{org, resumed.AccountID, resumed.ContactID}).lock(tx); err != nil {
			return err
		}
		snapshot, err := a.reviewInboxSequence(tx, org, actor, id)
		if err != nil {
			return err
		}
		if !snapshot.Preview.ActionsEligible || (snapshot.Run.SequenceSendReserved && snapshot.Preview.SendUnavailable) || !inboxPreviewMatches(snapshot.Confirmation, input.Confirmation) {
			return errInboxConflict
		}
		if operation != uuid.Nil {
			for _, prepared := range snapshot.Preview.Integrations {
				credential := ""
				if prepared.Definition.AuthMode != "none" {
					key, err := a.integrationEncryptionKey()
					if err != nil {
						return err
					}
					credential, err = crypto.Decrypt(prepared.Definition.Credential, key)
					if err != nil {
						return errInboxTransition
					}
				}
				if !safeInboxRecoveryText(input.Reason, 1000, credential, prepared.Endpoint) {
					return errInboxTransition
				}
			}
			before := models.InboxIntegrationExecution{State: snapshot.Run.State, Outcome: snapshot.Run.Outcome}
			decision = newInboxIntegrationRecovery(operation, inboxIntegrationRecoveryContext{Run: snapshot.Run, Case: snapshot.Case, Execution: before}, actor, "resume_sequence", input.Reason, digest)
		}
		previous, err := a.inboxSequencePlan(resumed)
		if err != nil {
			return err
		}
		if previous.Integrations == nil {
			previous.Integrations = map[int]inboxIntegrationApproval{}
		}
		if previous.DeferredActions == nil {
			previous.DeferredActions = map[int]bool{}
		}
		// Approve only the presently known remainder. Later dependencies stay
		// deferred even when their fields currently contain an older value.
		for index := range previous.DeferredActions {
			if index >= resumed.NextAction {
				delete(previous.DeferredActions, index)
			}
		}
		for index := range inboxSequenceDeferredActions(snapshot.Preview) {
			previous.DeferredActions[resumed.NextAction+index] = true
		}
		for index, prepared := range snapshot.Preview.Integrations {
			previous.Integrations[resumed.NextAction+index] = inboxIntegrationApproval{prepared.DefinitionDigest, prepared.RequestDigest}
		}
		previous.ReplyDigest = snapshot.Preview.Digest
		encrypted, err := a.encryptInboxSequencePlan(previous)
		if err != nil {
			return err
		}
		// Replace only a never-submitted current request after the fresh signed
		// review. Submitted/completed operation identities and bodies survive.
		if prepared, exists := snapshot.Preview.Integrations[0]; exists {
			var execution models.InboxIntegrationExecution
			// A dispatcher may already hold this execution while preparing its
			// result. Do not wait while holding the parent and organisation locks.
			err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&execution, "run_id=? AND action_index=? AND organization_id=?", id, resumed.NextAction, org).Error
			var lockError *pgconn.PgError
			if errors.As(err, &lockError) && lockError.Code == "55P03" {
				return errInboxConflict
			}
			if err == nil {
				if execution.AttemptCount != 0 || (execution.State != "pending" && execution.State != "blocked") {
					return errInboxConflict
				}
				key, err := a.integrationEncryptionKey()
				if err != nil {
					return err
				}
				raw, err := json.Marshal(integrationRequestSnapshot{snapshot.Workflow.Actions[resumed.NextAction].IntegrationInputs, prepared.Body})
				if err != nil {
					return err
				}
				ciphertext, err := crypto.Encrypt(string(raw), key)
				if err != nil {
					return err
				}
				digest, err := inboxSendCaseDigest(tx, snapshot.Case)
				if err != nil {
					return err
				}
				if err := tx.Model(&execution).Updates(map[string]any{"actor_id": actor, "state": "pending", "outcome": "", "definition_digest": prepared.DefinitionDigest, "request_digest": prepared.RequestDigest, "case_digest": digest, "encrypted_request": ciphertext}).Error; err != nil {
					return err
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		for index, outcome := range snapshot.Preview.ActionOutcomes {
			outcome.Index = resumed.NextAction + index
			outcome.Status = "pending"
			resumed.ActionOutcomes[resumed.NextAction+index] = outcome
		}
		resumed.ActorID, resumed.State, resumed.Outcome, resumed.EncryptedSequencePlan = actor, inboxSequenceRunning, "", encrypted
		resumed.CaseDigest, err = inboxSendCaseDigest(tx, snapshot.Case)
		if err != nil {
			return err
		}
		if err := saveInboxSequence(tx, &resumed); err != nil {
			return err
		}
		if err := tx.Model(&resumed).Updates(map[string]any{"actor_id": actor, "encrypted_sequence_plan": encrypted}).Error; err != nil {
			return err
		}
		if operation != uuid.Nil {
			if err := tx.Create(&decision).Error; err != nil {
				return err
			}
		}
		return appendInboxActivity(tx, snapshot.Case, models.InboxCaseActivity{Kind: "workflow_sequence_reviewed", ActorID: &actor, Detail: input.Reason})
	})
	if err != nil {
		return a.inboxError(r, inboxIntegrationPersistenceError(err))
	}
	out := map[string]any{"run": resumed}
	if operation != uuid.Nil {
		out["recovery"] = decision
	}
	return r.SendEnvelope(out)
}
