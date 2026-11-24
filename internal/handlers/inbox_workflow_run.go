package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
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
	Integrations          map[int]preparedInboxIntegration
	DeferredActions       map[int][]string
	ConditionOutcomes     []models.InboxConditionOutcome
	ActionOutcomes        []models.InboxActionOutcome
	ActionsEligible       bool
	Matches               bool
	Reply                 *inboxReplyPreview
	Digest                string
	Confirmation          string
	SendUnavailableReason string
	SendUnavailable       bool
}

func workflowDigest(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (a *App) evaluateInboxWorkflow(tx *gorm.DB, w models.InboxWorkflow, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, user uuid.UUID, now time.Time, triggeringText ...string) (inboxWorkflowPreview, error) {
	out := inboxWorkflowPreview{Matches: true}
	var err error
	out.ConditionOutcomes, err = a.inboxConditionOutcomes(tx, w, c, contact, user, triggeringText)
	if err != nil {
		return out, err
	}
	hasAny, anyMatched := false, false
	for _, condition := range out.ConditionOutcomes {
		if condition.Group == "any" {
			hasAny = true
			anyMatched = anyMatched || condition.Matched
		} else {
			out.Matches = out.Matches && condition.Matched
		}
	}
	if hasAny {
		out.Matches = out.Matches && anyMatched
	}
	out.Integrations = map[int]preparedInboxIntegration{}
	out.ActionOutcomes, out.ActionsEligible, err = a.preflightInboxActions(tx, w, c, user, now, out.Integrations)
	if err != nil {
		return out, err
	}
	out.DeferredActions = map[int][]string{}
	plannedVariables := map[string]bool{}
	for index, action := range w.Actions {
		if prepared, exists := out.Integrations[index]; exists {
			pending := map[string]bool{}
			for _, keys := range prepared.Intent.DeferredInputs {
				for _, key := range keys {
					pending[key] = true
				}
			}
			for key := range pending {
				out.DeferredActions[index] = append(out.DeferredActions[index], key)
			}
			sort.Strings(out.DeferredActions[index])
			if prepared.Intent.Available {
				for _, output := range prepared.Intent.Outputs {
					plannedVariables[output.Key] = true
				}
			}
		}
		if action.Type != "send_text" && action.Type != "send_template" {
			continue
		}
		pending, err := inboxDeferredDependencies(inboxActionPersonalisationTexts(action), plannedVariables)
		if err != nil {
			return out, err
		}
		if len(pending) != 0 {
			out.DeferredActions[index] = pending
			if out.ActionOutcomes[index].Status != "blocked" {
				out.ActionOutcomes[index].Status, out.ActionOutcomes[index].Reason = "deferred", "prior_integration_result_requires_review"
			}
		}
		reply, err := a.previewInboxReplyForActor(tx, c, account, contact, action, now, user, plannedVariables)
		reply.DeferredVariables = pending
		if errors.Is(err, errInboxSendIneligible) {
			out.SendUnavailable = true
			out.SendUnavailableReason = inboxEligibilityReason(err)
			out.Reply = &reply
		} else if err != nil {
			return out, err
		} else {
			out.Reply = &reply
			if len(pending) == 0 {
				out.Digest = inboxReplyFingerprint(c, account, contact, reply)
			}
		}
	}

	stateDigest, err := inboxSendCaseDigest(tx, c)
	if err != nil {
		return out, err
	}
	confirmationParts := []any{stateDigest, w.ID, w.UpdatedAt, c.ID, c.Version, user, out.Digest, out.SendUnavailable, out.ActionOutcomes}
	if len(out.Integrations) > 0 {
		confirmationParts = append(confirmationParts, inboxSequenceApprovals(out.Integrations))
	}
	if len(out.DeferredActions) != 0 {
		confirmationParts = append(confirmationParts, out.DeferredActions)
	}
	out.Confirmation = inboxPreviewConfirmation(confirmationParts)
	return out, nil
}

func inboxReplyFingerprint(c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, reply inboxReplyPreview) string {
	payload := []any{c.OrganizationID, account.ID, account.Name, account.PhoneID, contact.ID, contact.PhoneNumber, reply, reply.TemplateRevision}
	if reply.CannedRevision != "" {
		payload = append(payload, reply.CannedRevision)
	}
	return workflowDigest(payload)
}

func (a *App) workflowActorActive(db *gorm.DB, org, user uuid.UUID) bool {
	var count int64
	err := db.Model(&models.User{}).Where("id = ? AND is_active AND (is_super_admin OR organization_id = ? OR "+
		"EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.deleted_at IS NULL AND uo.user_id = users.id AND uo.organization_id = ?))", user, org, org).Count(&count).Error
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

func (a *App) runInboxWorkflowActions(tx *gorm.DB, c *models.InboxCase, contact *models.Contact, actor uuid.UUID, w models.InboxWorkflow, now time.Time, outcomes ...*[]models.InboxActionOutcome) error {
	for index, action := range w.Actions {
		switch action.Type {
		case "assign":
			if !a.HasPermission(actor, models.ResourceContacts, models.ActionWrite, c.OrganizationID) {
				return errInboxTransition
			}
			assignee, err := uuid.Parse(action.Value)
			if err != nil {
				return errInboxTransition
			}
			if err := assignInboxCase(tx, c, actor, assignee, ""); err != nil {
				return err
			}
		case "assign_team":
			if !a.HasPermission(actor, models.ResourceContacts, models.ActionWrite, c.OrganizationID) {
				return errInboxTransition
			}
			id, err := uuid.Parse(action.Value)
			if err != nil {
				return errInboxTransition
			}
			var team models.Team
			if err := tx.First(&team, "id=? AND organization_id=? AND is_active", id, c.OrganizationID).Error; err != nil {
				return err
			}
			if c.Status == models.InboxCaseResolved {
				return errInboxTransition
			}
			c.TeamID, c.AssigneeID, c.RoutingReason = &id, nil, "unavailable"
			c.RoutingNextAttemptAt = nil
			c.Version++
			if team.AssignmentStrategy == models.AssignmentStrategyManual {
				c.RoutingReason = "manual"
			}
			if err := tx.Save(c).Error; err != nil {
				return err
			}
			if err := appendInboxActivity(tx, *c, models.InboxCaseActivity{Kind: "assigned_team", ActorID: &actor}); err != nil {
				return err
			}
			if inboxCountsForCapacity(c.Status) && team.AssignmentStrategy != models.AssignmentStrategyManual {
				if err := a.routeEligibleInboxCase(tx, c); err != nil {
					return err
				}
			}
		case "snooze":
			until := now.Add(time.Duration(action.DurationMinutes) * time.Minute)
			if action.DurationMinutes < 1 || action.DurationMinutes > 43200 || c.Status == models.InboxCaseResolved {
				return errInboxTransition
			}
			if err := applyInboxTransition(tx, c, inboxCaseUpdate{Status: models.InboxCaseSnoozed, SnoozedUntil: &until}, &actor, now, "automation"); err != nil {
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
		if len(outcomes) > 0 && index < len(*outcomes[0]) {
			item := &(*outcomes[0])[index]
			item.Status = "applied"
			if action.Type == "assign_team" {
				if c.AssigneeID == nil {
					item.Status = "unassigned"
					item.Reason = "team_unavailable"
					if c.RoutingReason == "capacity" {
						item.Reason = "team_at_capacity"
					}
					if c.RoutingReason == "manual" {
						item.Reason = "manual_assignment_required"
					}
				} else {
					item.Reason = "team_strategy_selected_agent"
				}
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
			var scoped models.InboxCase
			return a.scopeInboxCases(tx.Model(&scoped), user, org).First(&scoped, "inbox_cases.id=?", run.CaseID).Error
		}
		if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		var workflow models.InboxWorkflow
		if err := a.visibleInboxWorkflows(tx, org, user).Clauses(clause.Locking{Strength: "SHARE"}).First(&workflow, "id = ? AND kind = 'macro'", id).Error; err != nil {
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
		if !preview.Matches || !preview.ActionsEligible || !inboxPreviewMatches(preview.Confirmation, input.Confirmation) || preview.SendUnavailable {
			return errInboxConflict
		}
		if preview.Reply != nil && !a.inboxDispatchAllowed() {
			return errInboxSendIneligible
		}
		if inboxWorkflowHasIntegration(workflow) {
			run = newInboxWorkflowRun(input.RunID, workflow, latest, user)
			return a.queueInboxSequence(tx, latest, &run, preview, preview.Reply != nil, "")
		}
		actualOutcomes := append([]models.InboxActionOutcome{}, preview.ActionOutcomes...)
		if err := a.runInboxWorkflowActions(tx, &latest, &contact, user, workflow, now, &actualOutcomes); err != nil {
			return err
		}
		run = newInboxWorkflowRun(input.RunID, workflow, latest, user)
		run.ActionOutcomes, run.ConditionsMatched = actualOutcomes, &preview.Matches
		run.ConditionOutcomes = preview.ConditionOutcomes
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
	if run.State == inboxRunPending {
		var err error
		run.CaseDigest, err = inboxSendCaseDigest(tx, c)
		if err != nil {
			return err
		}
	}
	for i := range run.ActionOutcomes {
		item := &run.ActionOutcomes[i]
		if run.Sequenced {
			item.Status = "pending"
		} else if run.State == inboxRunFailed {
			item.Status = "rolled_back"
		} else if run.ConditionsMatched != nil && !*run.ConditionsMatched {
			item.Status = "not_applied"
		} else if item.Type == "send_text" || item.Type == "send_template" {
			item.Status = run.State
			if run.Outcome != "" {
				item.Reason = run.Outcome
			}
		} else if item.Status == "ready" {
			item.Status = "applied"
		}
	}
	if err := tx.Create(run).Error; err != nil {
		return err
	}
	return appendInboxActivity(tx, c, models.InboxCaseActivity{Kind: "workflow_" + run.State, ActorID: &run.ActorID, Detail: run.WorkflowName})
}

// Snapshot after this workflow's own actions. Subsequent case, contact or field
// changes invalidate queued sends, including old runs without a snapshot.
func inboxSendCaseDigest(tx *gorm.DB, c models.InboxCase) (string, error) {
	var contact models.Contact
	if err := tx.First(&contact, "id = ? AND organization_id = ?", c.ContactID, c.OrganizationID).Error; err != nil {
		return "", err
	}
	var values []models.InboxFieldValue
	if err := tx.Where("organization_id = ? AND subject_id IN ?", c.OrganizationID, []uuid.UUID{c.ID, c.ContactID}).
		Order("definition_id, subject_id").Find(&values).Error; err != nil {
		return "", err
	}
	return workflowDigest([]any{c.Version, c.Status, c.Priority, c.AssigneeID, c.TeamID, contact.Tags, values}), nil
}

func (a *App) ListInboxWorkflowRuns(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	runs := []struct {
		models.InboxWorkflowRun
		ActorName     string `json:"actor_name"`
		AccountName   string `json:"account_name"`
		ContactName   string `json:"contact_name"`
		MessageStatus string `json:"message_status"`
	}{}
	cases := a.scopeInboxCaseHistory(a.DB.Model(&models.InboxCase{}), user, org).Select("inbox_cases.id")
	q := a.DB.Model(&models.InboxWorkflowRun{}).
		Joins("JOIN inbox_cases case_context ON case_context.id=inbox_workflow_runs.case_id AND case_context.organization_id=inbox_workflow_runs.organization_id AND case_context.account_id=inbox_workflow_runs.account_id AND case_context.contact_id=inbox_workflow_runs.contact_id AND case_context.deleted_at IS NULL").
		Where("inbox_workflow_runs.organization_id = ? AND inbox_workflow_runs.case_id IN (?)", org, cases)
	if !a.HasPermission(user, models.ResourceSettingsGeneral, models.ActionWrite, org) {
		q = q.Where("(actor_id = ? OR initiated_by_id = ?) AND kind = 'macro'", user, user)
	}
	if id := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("case_id"))); id != "" {
		caseID, err := uuid.Parse(id)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid case identifier", nil, "")
		}
		q = q.Where("case_id = ?", caseID)
	}
	if err := q.Joins("JOIN whatsapp_accounts wa ON wa.id=inbox_workflow_runs.account_id AND wa.organization_id=inbox_workflow_runs.organization_id AND wa.deleted_at IS NULL").
		Joins("JOIN contacts c ON c.id=inbox_workflow_runs.contact_id AND c.organization_id=inbox_workflow_runs.organization_id AND c.deleted_at IS NULL").
		Joins("LEFT JOIN users actor ON actor.id=inbox_workflow_runs.actor_id AND actor.deleted_at IS NULL AND EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.deleted_at IS NULL AND uo.user_id=actor.id AND uo.organization_id=inbox_workflow_runs.organization_id)").
		Joins("LEFT JOIN messages m ON m.id=inbox_workflow_runs.id AND m.organization_id=inbox_workflow_runs.organization_id AND m.contact_id=inbox_workflow_runs.contact_id AND m.whats_app_account=wa.name AND m.deleted_at IS NULL").
		Select("inbox_workflow_runs.*,COALESCE(actor.full_name,'') AS actor_name,wa.name AS account_name,c.profile_name AS contact_name,COALESCE(m.status,'') AS message_status").Order("inbox_workflow_runs.created_at DESC,inbox_workflow_runs.id").Offset(parseInboxQueuePage(r.RequestCtx.QueryArgs())).Limit(51).Scan(&runs).Error; err != nil {
		return a.inboxError(r, err)
	}
	more := len(runs) > 50
	if more {
		runs = runs[:50]
	}
	if a.ShouldMaskPhoneNumbers(org) {
		for i := range runs {
			runs[i].ContactName = utils.MaskIfPhoneNumber(runs[i].ContactName)
		}
	}
	return r.SendEnvelope(map[string]any{"runs": runs, "has_more": more})
}
