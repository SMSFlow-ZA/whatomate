package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"gorm.io/gorm"
)

func inboxIntegrationValueHasPhone(value json.RawMessage) bool {
	if len(value) == 0 || string(value) == "null" {
		return false
	}
	text := strings.TrimSpace(string(value))
	var decoded string
	if json.Unmarshal(value, &decoded) == nil {
		text = decoded
	}
	return utils.MaskIfPhoneNumber(text) != text
}

func maskedInboxIntegrationValue(value json.RawMessage) json.RawMessage {
	if !inboxIntegrationValueHasPhone(value) {
		return value
	}
	text := strings.TrimSpace(string(value))
	var decoded string
	if json.Unmarshal(value, &decoded) == nil {
		text = decoded
	}
	raw, _ := json.Marshal(utils.MaskIfPhoneNumber(text))
	return raw
}

func (a *App) previewInboxIntegrationOutputChanges(db *gorm.DB, actor uuid.UUID, out *inboxIntegrationRecoveryPreview) error {
	c := out.Context
	e, run := c.Execution, c.Run
	if !c.DefinitionMatched {
		out.Reason = "definition_changed"
		return nil
	}
	if !run.Sequenced || run.NextAction != e.ActionIndex || (run.State != inboxSequenceRunning && run.State != inboxSequenceReview && run.State != inboxSequenceBlocked && run.State != inboxSequenceUnknown) {
		out.Reason = "workflow_stopped"
		return nil
	}
	latest, err := (inboxThread{e.OrganizationID, e.AccountID, e.ContactID}).latest(db)
	if err != nil {
		return err
	}
	if latest.ID != e.CaseID {
		out.Reason = "case_changed"
		return nil
	}
	var workflow models.InboxWorkflow
	if err := a.visibleInboxWorkflows(db, e.OrganizationID, actor).First(&workflow, "id=?", run.WorkflowID).Error; err != nil {
		return err
	}
	if !workflow.UpdatedAt.Equal(run.WorkflowUpdatedAt) || (workflow.Kind == "rule" && workflow.EnabledAt == nil) {
		out.Reason = "workflow_changed"
		return nil
	}
	fields, err := a.integrationOutputFields(db, c.Definition, actor)
	if err != nil {
		out.Reason = integrationProblemReason(err)
		return nil
	}
	key, err := a.integrationEncryptionKey()
	if err != nil {
		out.Reason = "encryption_unavailable"
		return nil
	}
	if !crypto.IsEncrypted(e.EncryptedResult) {
		out.Reason = "mapped_result_unavailable"
		return nil
	}
	raw, err := crypto.Decrypt(e.EncryptedResult, key)
	if err != nil || len(raw) > inboxintegration.MaxBody {
		out.Reason = "result_unavailable"
		return nil
	}
	var values map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &values) != nil {
		out.Reason = "result_invalid"
		return nil
	}
	out.Selected, err = selectInboxIntegrationResult(c.Definition, fields, values, c.Credential, true)
	if err != nil {
		out.Reason = "result_invalid"
		return nil
	}
	out.SchemaDigest = workflowDigest(fields)
	mask, err := inboxMessageMasking(db, e.OrganizationID)
	if err != nil {
		return err
	}
	for _, mapping := range c.Definition.Outputs {
		field := fields[mapping.DefinitionID]
		subject := e.CaseID
		if field.Scope == "contact" {
			subject = e.ContactID
		}
		var current models.InboxFieldValue
		err := db.First(&current, "organization_id=? AND definition_id=? AND subject_id=?", e.OrganizationID, field.ID, subject).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		proposed := out.Selected[mapping.Name]
		if inboxIntegrationResultContainsCredential(current.Value, c.Credential) {
			out.Fields = []inboxIntegrationFieldChange{}
			out.Reason = "sensitive_current_value"
			return nil
		}
		change := inboxIntegrationFieldChange{ID: field.ID, Label: field.Label, Scope: field.Scope, Kind: field.Kind, Current: current.Value, Proposed: proposed}
		out.Fields = append(out.Fields, change)
		if mask && (inboxIntegrationValueHasPhone(current.Value) || inboxIntegrationValueHasPhone(proposed)) {
			out.Reason = "masked_output_requires_authorised_review"
		}
	}
	return nil
}

type inboxIntegrationOutputReview struct {
	ActorID    uuid.UUID
	CaseDigest string
}

func (a *App) handleReviewedInboxIntegrationOutputs(tx *gorm.DB, out *inboxIntegrationRecoveryPreview, actor uuid.UUID, action string, now time.Time) error {
	c := out.Context
	e, run := c.Execution, c.Run
	marker := "outputs_discarded_at"
	if action == "apply_outputs" {
		applied, err := a.applyInboxIntegrationOutputs(tx, e, now, inboxIntegrationOutputReview{actor, c.CaseDigest})
		if err != nil {
			return err
		}
		if !applied {
			return errInboxConflict
		}
		marker = "outputs_applied_at"
	}
	if err := tx.Model(&e).Updates(map[string]any{marker: now, "reconciled_by_id": actor}).Error; err != nil {
		return err
	}
	// A reviewed result never implicitly approves a new reply or external call.
	// Only the current integration step advances; all remaining steps pause.
	if run.Sequenced && run.NextAction == e.ActionIndex && run.State != inboxSequenceCancelled && run.State != inboxRunApplied && run.State != inboxRunSent && run.State != inboxSequenceSending {
		latest, err := (inboxThread{e.OrganizationID, e.AccountID, e.ContactID}).latest(tx)
		if err != nil {
			return err
		}
		if latest.ID != e.CaseID {
			return stopInboxSequence(tx, &run, inboxSequenceBlocked, "case_changed")
		}
		if run.NextAction < 0 || run.NextAction >= len(run.ActionOutcomes) {
			return errInboxConflict
		}
		run.ActionOutcomes[run.NextAction].Status = "succeeded"
		run.ActionOutcomes[run.NextAction].Reason = "reviewed_integration_outputs_applied"
		if action == "discard_outputs" {
			run.ActionOutcomes[run.NextAction].Reason = "integration_outputs_discarded"
		}
		run.NextAction++
		if run.InitiatedByID == nil {
			original := run.ActorID
			run.InitiatedByID = &original
		}
		run.ActorID = actor
		run.CaseDigest, err = inboxSendCaseDigest(tx, latest)
		if err != nil {
			return err
		}
		run.State, run.Outcome = inboxSequenceReview, "remaining_actions_require_review"
		if run.NextAction == len(run.ActionOutcomes) {
			run.State, run.Outcome = inboxRunApplied, ""
			for _, step := range run.ActionOutcomes {
				if step.Status == inboxRunSent {
					run.State = inboxRunSent
				}
			}
		}
		if err := saveInboxSequence(tx, &run); err != nil {
			return err
		}
		return tx.Model(&run).Updates(map[string]any{"actor_id": actor, "initiated_by_id": run.InitiatedByID}).Error
	}
	return nil
}
