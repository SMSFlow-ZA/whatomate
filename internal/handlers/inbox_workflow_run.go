package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	inboxRunApplied    = "applied"
	inboxRunPending    = "pending"
	inboxRunSubmitting = "submitting"
	inboxRunSent       = "sent"
	inboxRunUnknown    = "unknown"
	inboxRunBlocked    = "blocked"
	inboxRunFailed     = "failed"
)

type inboxWorkflowPreview struct {
	Matches         bool
	Reply           *inboxReplyPreview
	Digest          string
	Confirmation    string
	SendUnavailable bool
}

func workflowDigest(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (a *App) evaluateInboxWorkflow(tx *gorm.DB, w models.InboxWorkflow, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, user uuid.UUID, now time.Time) (inboxWorkflowPreview, error) {
	out := inboxWorkflowPreview{Matches: workflowMatchesCase(w.Conditions, c, contact)}

	for _, action := range w.Actions {
		if action.Type != "send_text" && action.Type != "send_template" {
			continue
		}
		reply, err := previewInboxReplyWithDB(tx, c, account, contact, action, now)
		if errors.Is(err, errInboxSendIneligible) {
			out.SendUnavailable = true
		} else if err != nil {
			return out, err
		} else {
			out.Reply = &reply
			out.Digest = inboxReplyFingerprint(c, account, contact, reply)
		}
	}
	out.Confirmation = workflowDigest([]any{w.ID, w.UpdatedAt, c.ID, c.Version, user, out.Digest, out.SendUnavailable})
	return out, nil
}

func inboxReplyFingerprint(c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, reply inboxReplyPreview) string {
	return workflowDigest([]any{c.OrganizationID, account.ID, account.Name, account.PhoneID, contact.ID, contact.PhoneNumber, reply})
}

func (a *App) workflowActorActive(db *gorm.DB, org, user uuid.UUID) bool {
	var count int64
	err := db.Model(&models.User{}).Where("id = ? AND is_active AND (is_super_admin OR organization_id = ? OR "+
		"EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.user_id = users.id AND uo.organization_id = ?))", user, org, org).Count(&count).Error
	return err == nil && count == 1
}

func (a *App) inboxWorkflowContext(tx *gorm.DB, c models.InboxCase) (models.WhatsAppAccount, models.Contact, error) {
	var account models.WhatsAppAccount
	var contact models.Contact
	if err := tx.First(&account, "id = ? AND organization_id = ?", c.AccountID, c.OrganizationID).Error; err != nil {
		return account, contact, err
	}
	err := tx.First(&contact, "id = ? AND organization_id = ?", c.ContactID, c.OrganizationID).Error
	return account, contact, err
}

func (a *App) runInboxWorkflowActions(tx *gorm.DB, c *models.InboxCase, contact *models.Contact, actor uuid.UUID, w models.InboxWorkflow, now time.Time) error {
	for _, action := range w.Actions {
		switch action.Type {
		case "assign":
			if !a.HasPermission(actor, models.ResourceContacts, models.ActionWrite, c.OrganizationID) {
				return errInboxTransition
			}
			assignee, err := uuid.Parse(action.Value)
			if err != nil {
				return errInboxTransition
			}
			if err := assignInboxCase(tx, c, actor, assignee); err != nil {
				return err
			}
		case "tag":
			if !a.HasPermission(actor, models.ResourceContacts, models.ActionWrite, c.OrganizationID) {
				return errInboxTransition
			}
			if err := tagInboxCase(tx, c, contact, actor, action.Value); err != nil {
				return err
			}
		case "priority", "status":
			var update inboxCaseUpdate
			if action.Type == "priority" {
				update.Priority = models.InboxCasePriority(action.Value)
			} else {
				update.Status = models.InboxCaseStatus(action.Value)
			}
			if !update.valid(now) || c.Status == models.InboxCaseResolved {
				return errInboxTransition
			}
			if err := applyInboxTransition(tx, c, update, &actor, now, "automation"); err != nil {
				return err
			}
		case "note":
			if err := appendInboxActivity(tx, *c, models.InboxCaseActivity{Kind: "workflow_note", ActorID: &actor, Detail: action.Value, OccurredAt: &now}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) ExecuteInboxMacro(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionWrite)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "Macro")
	if err != nil {
		return nil
	}
	var input struct {
		CaseID       uuid.UUID `json:"case_id"`
		RunID        uuid.UUID `json:"run_id"`
		Confirmation string    `json:"confirmation"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if input.CaseID == uuid.Nil || input.RunID == uuid.Nil || len(input.Confirmation) != 64 {
		return a.inboxError(r, errInboxTransition)
	}
	var run models.InboxWorkflowRun
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, org, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxDisabled
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-macro:"+input.RunID.String()).Error; err != nil {
			return err
		}
		lookup := tx.Where("id = ?", input.RunID).First(&run).Error
		if lookup == nil {
			if run.OrganizationID != org || run.ActorID != user || run.CaseID != input.CaseID || run.WorkflowID != id || run.Kind != "macro" {
				return errInboxConflict
			}
			return nil
		}
		if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		var workflow models.InboxWorkflow
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&workflow, "id = ? AND organization_id = ? AND kind = 'macro'", id, org).Error; err != nil {
			return err
		}
		var c models.InboxCase
		if err := a.scopeInboxCases(tx.Model(&c), user, org).First(&c, "inbox_cases.id = ?", input.CaseID).Error; err != nil {
			return err
		}
		thread := inboxThread{org, c.AccountID, c.ContactID}
		if err := thread.lock(tx); err != nil {
			return err
		}
		latest, err := thread.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		if latest.ID != c.ID || latest.Status == models.InboxCaseResolved {
			return errInboxConflict
		}
		if err := a.scopeInboxCases(tx.Model(&c), user, org).First(&c, "inbox_cases.id = ?", input.CaseID).Error; err != nil {
			return err
		}
		account, contact, err := a.inboxWorkflowContext(tx, latest)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		preview, err := a.evaluateInboxWorkflow(tx, workflow, latest, account, contact, user, now)
		if err != nil {
			return err
		}
		if !preview.Matches || preview.Confirmation != input.Confirmation || preview.SendUnavailable {
			return errInboxConflict
		}
		if preview.Digest != "" && !a.inboxDispatchAllowed() {
			return errInboxSendIneligible
		}
		if err := a.runInboxWorkflowActions(tx, &latest, &contact, user, workflow, now); err != nil {
			return err
		}
		run = newInboxWorkflowRun(input.RunID, workflow, latest, user)
		if preview.Digest != "" {
			run.State, run.SendDigest = inboxRunPending, preview.Digest
		}
		return saveInboxWorkflowRun(tx, latest, &run)
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"run": run})
}

func newInboxWorkflowRun(id uuid.UUID, w models.InboxWorkflow, c models.InboxCase, actor uuid.UUID) models.InboxWorkflowRun {
	return models.InboxWorkflowRun{
		BaseModel: models.BaseModel{ID: id}, OrganizationID: c.OrganizationID, AccountID: c.AccountID, ContactID: c.ContactID,
		CaseID: c.ID, WorkflowID: w.ID, WorkflowName: w.Name, WorkflowUpdatedAt: w.UpdatedAt,
		ActorID: actor, Kind: w.Kind, State: inboxRunApplied,
	}
}

func saveInboxWorkflowRun(tx *gorm.DB, c models.InboxCase, run *models.InboxWorkflowRun) error {
	if err := tx.Create(run).Error; err != nil {
		return err
	}
	return appendInboxActivity(tx, c, models.InboxCaseActivity{Kind: "workflow_" + run.State, ActorID: &run.ActorID, Detail: run.WorkflowName})
}

func (a *App) ListInboxWorkflowRuns(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	var runs []models.InboxWorkflowRun
	cases := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).Select("inbox_cases.id")
	q := a.DB.Where("organization_id = ? AND case_id IN (?)", org, cases)
	if !a.HasPermission(user, models.ResourceSettingsGeneral, models.ActionWrite, org) {
		q = q.Where("actor_id = ? AND kind = 'macro'", user)
	}
	if id := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("case_id"))); id != "" {
		caseID, err := uuid.Parse(id)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid case identifier", nil, "")
		}
		q = q.Where("case_id = ?", caseID)
	}
	if err := q.Order("created_at DESC, id").Limit(50).Find(&runs).Error; err != nil {
		return a.inboxError(r, err)
	}
	if runs == nil {
		runs = []models.InboxWorkflowRun{}
	}
	return r.SendEnvelope(map[string]any{"runs": runs})
}
