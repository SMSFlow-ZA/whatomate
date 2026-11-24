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
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxIntegrationRecoveryInput struct {
	OperationID  uuid.UUID                  `json:"operation_id"`
	Confirmation string                     `json:"confirmation"`
	Action       string                     `json:"action"`
	Reason       string                     `json:"reason"`
	Reference    string                     `json:"evidence_reference"`
	Result       map[string]json.RawMessage `json:"result"`
}

type inboxIntegrationRecoveryContext struct {
	Execution         models.InboxIntegrationExecution
	Run               models.InboxWorkflowRun
	Case              models.InboxCase
	CaseDigest        string
	Definition        models.InboxIntegration
	Endpoint          inboxintegration.Endpoint
	Credential        string
	DefinitionMatched bool
}

type inboxIntegrationFieldChange struct {
	ID       uuid.UUID       `json:"id"`
	Label    string          `json:"label"`
	Scope    string          `json:"scope"`
	Kind     string          `json:"kind"`
	Current  json.RawMessage `json:"current"`
	Proposed json.RawMessage `json:"proposed"`
}

type inboxIntegrationRecoveryPreview struct {
	Context      inboxIntegrationRecoveryContext
	Selected     map[string]json.RawMessage
	Fields       []inboxIntegrationFieldChange
	SchemaDigest string
	Confirmation string
	Reason       string
}

func inboxRecoveryLockError(err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "55P03" {
		return errInboxConflict
	}
	return err
}

func (a *App) inboxIntegrationRecoveryContext(db *gorm.DB, org, actor, id uuid.UUID) (inboxIntegrationRecoveryContext, error) {
	var out inboxIntegrationRecoveryContext
	if err := a.scopedInboxIntegrationHistory(db, org, actor).First(&out.Execution, "id=?", id).Error; err != nil {
		return out, err
	}
	if err := db.First(&out.Run, "id=? AND organization_id=?", out.Execution.RunID, org).Error; err != nil {
		return out, err
	}
	if err := a.scopeInboxCaseHistory(db.Model(&models.InboxCase{}), actor, org).First(&out.Case, "inbox_cases.id=?", out.Execution.CaseID).Error; err != nil {
		return out, err
	}
	var err error
	out.CaseDigest, err = inboxSendCaseDigest(db, out.Case)
	if err != nil {
		return out, err
	}
	err = db.First(&out.Definition, "id=? AND organization_id=?", out.Execution.IntegrationID, org).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if out.Definition.AuthMode != "none" {
		key, err := a.integrationEncryptionKey()
		if err != nil {
			return out, errInboxTransition
		}
		out.Credential, err = crypto.Decrypt(out.Definition.Credential, key)
		if err != nil {
			return out, errInboxTransition
		}
	}
	out.Endpoint, err = a.integrationEndpoint(out.Definition.EndpointKey, org)
	if err != nil {
		return out, nil
	} // Recording a fact need not reactivate a removed endpoint.
	out.DefinitionMatched = integrationDefinitionDigest(out.Definition, out.Endpoint) == out.Execution.DefinitionDigest
	return out, nil
}

func safeInboxRecoveryText(value string, max int, credential string, endpoint inboxintegration.Endpoint) bool {
	if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > max || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if credential != "" && strings.Contains(value, credential) {
		return false
	}
	if endpoint.URL != "" && strings.Contains(value, endpoint.URL) {
		return false
	}
	if u, err := url.Parse(endpoint.URL); err == nil && u.Path != "" && u.Path != "/" && strings.Contains(value, u.Path) {
		return false
	}
	return true
}

func (a *App) previewInboxIntegrationRecovery(db *gorm.DB, org, actor, id uuid.UUID, input inboxIntegrationRecoveryInput) (inboxIntegrationRecoveryPreview, error) {
	out := inboxIntegrationRecoveryPreview{Fields: []inboxIntegrationFieldChange{}}
	var err error
	out.Context, err = a.inboxIntegrationRecoveryContext(db, org, actor, id)
	if err != nil {
		return out, err
	}
	c := out.Context
	switch input.Action {
	case "confirm_succeeded", "confirm_not_applied":
		if c.Execution.AttemptCount < 1 || (c.Execution.State != "unknown" && c.Execution.State != "failed") {
			out.Reason = "not_reconcilable_state"
			return out, nil
		}
		if !safeInboxRecoveryText(input.Reference, 200, c.Credential, c.Endpoint) {
			out.Reason = "investigation_reference_required"
			return out, nil
		}
		if input.Action == "confirm_not_applied" && len(input.Result) != 0 {
			out.Reason = "result_not_allowed"
			return out, nil
		}
		if input.Action == "confirm_succeeded" && input.Result != nil {
			if !c.DefinitionMatched {
				if len(input.Result) != 0 {
					out.Reason = "definition_changed_record_fact_only"
					return out, nil
				}
			} else {
				fields, err := a.integrationOutputFields(db, c.Definition, actor)
				if err != nil {
					out.Reason = integrationProblemReason(err)
					return out, nil
				}
				out.Selected, err = selectInboxIntegrationResult(c.Definition, fields, input.Result, c.Credential, true)
				if err != nil {
					out.Reason = "result_invalid"
					return out, nil
				}
				out.SchemaDigest = workflowDigest(fields)
				for _, mapping := range c.Definition.Outputs {
					field := fields[mapping.DefinitionID]
					out.Fields = append(out.Fields, inboxIntegrationFieldChange{ID: field.ID, Label: field.Label, Scope: field.Scope, Kind: field.Kind, Proposed: out.Selected[mapping.Name]})
				}
			}
		}
	case "apply_outputs", "discard_outputs":
		if input.Reference != "" || input.Result != nil {
			out.Reason = "result_not_allowed"
			return out, nil
		}
		if c.Execution.State != "succeeded" || c.Execution.OutputsAppliedAt != nil || c.Execution.OutputsDiscardedAt != nil {
			out.Reason = "no_unhandled_success"
			return out, nil
		}
		if input.Action == "apply_outputs" {
			if err := a.previewInboxIntegrationOutputChanges(db, actor, &out); err != nil {
				return out, err
			}
			if out.Reason != "" {
				return out, nil
			}
		}
	default:
		return out, errInboxTransition
	}
	out.Confirmation = inboxPreviewConfirmation([]any{"integration-recovery", actor, input.Action, input.Reference, workflowDigest(out.Selected), out.SchemaDigest, c.Execution.ID, c.Execution.UpdatedAt, c.Execution.State, c.Execution.Outcome, c.Execution.AttemptCount, c.Execution.ResultOrigin, workflowDigest(c.Execution.EncryptedResult), c.Execution.OutputsAppliedAt, c.Execution.OutputsDiscardedAt, c.Execution.DefinitionDigest, c.Run.ID, c.Run.UpdatedAt, c.Run.State, c.Run.NextAction, c.Run.ActorID, c.CaseDigest})
	return out, nil
}

func (a *App) PreviewInboxIntegrationRecovery(r *fastglue.Request) error {
	org, actor, ok := a.integrationRecoveryAuth(r)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "execution_id", "Integration execution")
	if err != nil {
		return nil
	}
	var input inboxIntegrationRecoveryInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Reference = strings.TrimSpace(input.Reference)
	out, err := a.previewInboxIntegrationRecovery(a.DB, org, actor, id, input)
	if err != nil {
		return a.inboxError(r, err)
	}
	mask, err := inboxMessageMasking(a.DB, org)
	if err != nil {
		return a.inboxError(r, err)
	}
	if mask {
		for i := range out.Fields {
			out.Fields[i].Current = maskedInboxIntegrationValue(out.Fields[i].Current)
			out.Fields[i].Proposed = maskedInboxIntegrationValue(out.Fields[i].Proposed)
		}
		input.Reference = utils.MaskIfPhoneNumber(input.Reference)
	}
	if !safeInboxRecoveryText(input.Reference, 200, out.Context.Credential, out.Context.Endpoint) {
		input.Reference = ""
	}
	resultReference := out.Selected[out.Context.Definition.ReferenceKey]
	if mask {
		resultReference = maskedInboxIntegrationValue(resultReference)
	}
	return r.SendEnvelope(map[string]any{"execution_id": id, "run_id": out.Context.Run.ID, "case_id": out.Context.Case.ID, "case_version": out.Context.Case.Version, "account_id": out.Context.Case.AccountID, "action": input.Action, "eligible": out.Confirmation != "", "reason": out.Reason, "confirmation": out.Confirmation, "field_changes": out.Fields, "evidence_reference": input.Reference, "result_reference": resultReference, "result_origin": out.Context.Execution.ResultOrigin, "manual_attestation": input.Action == "confirm_succeeded" || input.Action == "confirm_not_applied", "fact_only": out.Selected == nil, "remaining_steps_require_review": true})
}

func newInboxIntegrationRecovery(operation uuid.UUID, c inboxIntegrationRecoveryContext, actor uuid.UUID, kind, reason, digest string) models.InboxIntegrationRecovery {
	runID := c.Run.ID
	e := c.Execution
	return models.InboxIntegrationRecovery{BaseModel: models.BaseModel{ID: operation}, OrganizationID: c.Run.OrganizationID, ExecutionID: e.ID, RunID: &runID, ActorID: actor, Kind: kind, FromState: e.State, FromOutcome: e.Outcome, FromResultOrigin: e.ResultOrigin, AttemptCount: e.AttemptCount, PriorStartedAt: e.StartedAt, PriorCompletedAt: e.CompletedAt, ProviderStatus: e.ProviderStatus, DurationMillis: e.DurationMillis, Reason: reason, RequestDigest: digest}
}

// All operational recovery activity is non-triggering and changes no SLA clock.
func recordInboxIntegrationRecoveryActivity(tx *gorm.DB, c models.InboxCase, actor uuid.UUID, kind, detail string) error {
	now := time.Now().UTC()
	return tx.Create(&models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: c.OrganizationID, AccountID: c.AccountID, CaseID: c.ID, ActorID: &actor, Kind: kind, Detail: detail, OccurredAt: &now, AssigneeID: c.AssigneeID, TeamID: c.TeamID}).Error
}

func (a *App) RecoverInboxIntegration(r *fastglue.Request) error {
	org, actor, ok := a.integrationRecoveryAuth(r)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "execution_id", "Integration execution")
	if err != nil {
		return nil
	}
	var input inboxIntegrationRecoveryInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Reason, input.Reference = strings.TrimSpace(input.Reason), strings.TrimSpace(input.Reference)
	if input.OperationID == uuid.Nil || len(input.Confirmation) != 64 || !safeInboxRecoveryText(input.Reason, 1000, "", inboxintegration.Endpoint{}) {
		return a.inboxError(r, errInboxTransition)
	}
	requestDigest := workflowDigest([]any{org, actor, id, input.Action, input.Reason, input.Reference, input.Result, input.Confirmation})
	var decision models.InboxIntegrationRecovery
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-integration-recovery:"+input.OperationID.String()).Error; err != nil {
			return err
		}
		var execution models.InboxIntegrationExecution
		if err := a.scopedInboxIntegrationHistory(tx, org, actor).First(&execution, "id=?", id).Error; err != nil {
			return err
		}
		err := tx.First(&decision, "id=?", input.OperationID).Error
		if err == nil {
			if decision.OrganizationID != org || decision.ExecutionID != id || decision.ActorID != actor || decision.RequestDigest != requestDigest {
				return errInboxConflict
			}
			decision.HasEvidence = decision.EncryptedEvidence != ""
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Nonblocking locks prevent cycles with dispatch finalisation, context
		// review and human-reply recording. A busy record requires a new review.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&execution, "id=? AND organization_id=?", id, org).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		var organization models.Organization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Select("id").First(&organization, "id=?", org).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		var run models.InboxWorkflowRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&run, "id=? AND organization_id=?", execution.RunID, org).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		var contact models.Contact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Select("id").First(&contact, "id=? AND organization_id=?", execution.ContactID, org).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		var current models.InboxCase
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&current, "id=? AND organization_id=?", execution.CaseID, org).Error; err != nil {
			return inboxRecoveryLockError(err)
		}
		out, err := a.previewInboxIntegrationRecovery(tx, org, actor, id, input)
		if err != nil {
			return err
		}
		if out.Confirmation == "" || !inboxPreviewMatches(out.Confirmation, input.Confirmation) {
			return errInboxConflict
		}
		if !safeInboxRecoveryText(input.Reason, 1000, out.Context.Credential, out.Context.Endpoint) {
			return errInboxTransition
		}
		decision = newInboxIntegrationRecovery(input.OperationID, out.Context, actor, input.Action, input.Reason, requestDigest)
		now := time.Now().UTC()
		switch input.Action {
		case "confirm_succeeded", "confirm_not_applied":
			key, err := a.integrationEncryptionKey()
			if err != nil {
				return errInboxTransition
			}
			evidence, _ := json.Marshal(map[string]string{"reference": input.Reference})
			decision.EncryptedEvidence, err = crypto.Encrypt(string(evidence), key)
			if err != nil {
				return err
			}
			decision.HasEvidence = true
			result := ""
			if out.Selected != nil {
				raw, err := json.Marshal(out.Selected)
				if err != nil {
					return err
				}
				result, err = crypto.Encrypt(string(raw), key)
				if err != nil {
					return err
				}
			}
			state, outcome := "succeeded", "manually_confirmed_success"
			if input.Action == "confirm_not_applied" {
				state, outcome = "failed", "manually_confirmed_no_effect"
			}
			if err := tx.Model(&execution).Updates(map[string]any{"state": state, "outcome": outcome, "result_origin": "manual", "encrypted_result": result, "retry_requested": false, "completed_at": now, "reconciled_by_id": actor, "reconciliation_reason": input.Reason}).Error; err != nil {
				return err
			}
			// Recording an external fact is not permission to resume local work.
			if run.Sequenced && run.NextAction == execution.ActionIndex && (run.State == inboxSequenceRunning || run.State == inboxSequenceReview || run.State == inboxSequenceUnknown || run.State == inboxSequenceBlocked) {
				pauseState, pauseReason := inboxSequenceReview, "output_review_required"
				if input.Action == "confirm_not_applied" {
					pauseState, pauseReason = inboxSequenceBlocked, "provider_confirmed_no_effect"
				}
				if err := stopInboxSequence(tx, &run, pauseState, pauseReason); err != nil {
					return err
				}
			}
		case "apply_outputs", "discard_outputs":
			if err := a.handleReviewedInboxIntegrationOutputs(tx, &out, actor, input.Action, now); err != nil {
				return err
			}
		default:
			return errInboxTransition
		}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		return recordInboxIntegrationRecoveryActivity(tx, out.Context.Case, actor, "integration_recovered", input.Action)
	})
	if err != nil {
		return a.inboxError(r, inboxIntegrationPersistenceError(err))
	}
	return r.SendEnvelope(map[string]any{"recovery": decision, "external_call_made": false})
}

func (a *App) GetInboxIntegrationRecoveryEvidence(r *fastglue.Request) error {
	org, actor, ok := a.integrationRecoveryAuth(r)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "execution_id", "Integration execution")
	if err != nil {
		return nil
	}
	recoveryID, err := parsePathUUID(r, "recovery_id", "Recovery decision")
	if err != nil {
		return nil
	}
	var execution models.InboxIntegrationExecution
	if err := a.scopedInboxIntegrationHistory(a.DB, org, actor).First(&execution, "id=?", id).Error; err != nil {
		return a.inboxError(r, err)
	}
	var decision models.InboxIntegrationRecovery
	if err := a.DB.First(&decision, "id=? AND organization_id=? AND execution_id=?", recoveryID, org, id).Error; err != nil {
		return a.inboxError(r, err)
	}
	key, err := a.integrationEncryptionKey()
	if err != nil || !crypto.IsEncrypted(decision.EncryptedEvidence) {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "Recovery evidence is unavailable", nil, "integration_evidence_unavailable")
	}
	raw, err := crypto.Decrypt(decision.EncryptedEvidence, key)
	var evidence struct {
		Reference string `json:"reference"`
	}
	if err != nil || len(raw) > 2048 || json.Unmarshal([]byte(raw), &evidence) != nil || !safeInboxRecoveryText(evidence.Reference, 200, "", inboxintegration.Endpoint{}) {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "Recovery evidence is unavailable", nil, "integration_evidence_unavailable")
	}
	mask, err := inboxMessageMasking(a.DB, org)
	if err != nil {
		return a.inboxError(r, err)
	}
	if mask {
		evidence.Reference = utils.MaskIfPhoneNumber(evidence.Reference)
	}
	return r.SendEnvelope(map[string]any{"recovery_id": recoveryID, "evidence_reference": evidence.Reference, "source": "manual_attestation", "recorded_by": decision.ActorID})
}
