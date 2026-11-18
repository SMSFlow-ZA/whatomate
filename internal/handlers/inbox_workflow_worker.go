package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func inboxEventTriggers(event models.InboxCaseActivity) []string {
	if event.WorkflowTrigger == "entry_message" {
		return []string{"case_opened", "incoming_message"}
	}
	return []string{event.WorkflowTrigger}
}

func (a *App) processInboxRuleEvent(ctx context.Context, event models.InboxCaseActivity) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, event.OrganizationID, true)
		if err != nil || !enabled {
			return err
		}
		var c models.InboxCase
		err = tx.First(&c, "id = ? AND organization_id = ? AND account_id = ?", event.CaseID, event.OrganizationID, event.AccountID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		thread := inboxThread{c.OrganizationID, c.AccountID, c.ContactID}
		if err := thread.lock(tx); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&event, "id = ? AND organization_id = ?", event.ID, thread.org).Error; err != nil {
			return err
		}
		if event.WorkflowProcessedAt != nil {
			return nil
		}
		var previous int64
		if err := tx.Model(&models.InboxCaseActivity{}).
			Where("case_id = ? AND organization_id = ? AND workflow_trigger <> '' AND workflow_processed_at IS NULL", c.ID, c.OrganizationID).
			Where("(created_at, id) < (?, ?)", event.CreatedAt, event.ID).Count(&previous).Error; err != nil || previous != 0 {
			return err
		}
		now := time.Now().UTC()
		latest, err := thread.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		if latest.ID == c.ID {
			if err := a.applyInboxRules(tx, event, latest, now); err != nil {
				return err
			}
		}
		return tx.Model(&event).Update("workflow_processed_at", now).Error
	})
}

func (a *App) applyInboxRules(tx *gorm.DB, event models.InboxCaseActivity, c models.InboxCase, now time.Time) error {
	var workflows []models.InboxWorkflow
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Where("organization_id = ? AND kind = 'rule' AND trigger IN ? AND enabled_at <= ?",
			c.OrganizationID, inboxEventTriggers(event), event.CreatedAt).
		Order("position, created_at, id").Find(&workflows).Error; err != nil {
		return err
	}
	if len(workflows) == 0 {
		return nil
	}
	account, contact, err := a.inboxWorkflowContext(tx, c)
	if err != nil {
		return err
	}
	var org models.Organization
	if err := tx.Select("inbox_automation_sends_enabled").First(&org, "id = ?", c.OrganizationID).Error; err != nil {
		return err
	}
	sendSelected := false
	for _, w := range workflows {
		user := w.CreatedByID
		var scoped models.InboxCase
		if !a.workflowActorActive(tx, c.OrganizationID, user) ||
			!a.HasPermission(user, models.ResourceSettingsGeneral, models.ActionWrite, c.OrganizationID) ||
			!a.HasPermission(user, models.ResourceChat, models.ActionWrite, c.OrganizationID) {
			continue
		}
		err := a.scopeInboxCases(tx.Model(&scoped), user, c.OrganizationID).First(&scoped, "inbox_cases.id = ?", c.ID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		preview, err := a.evaluateInboxWorkflow(tx, w, c, account, contact, user, now)
		if err != nil {
			return err
		}
		if !preview.Matches {
			continue
		}
		run := newInboxWorkflowRun(uuid.New(), w, c, user)
		run.EventID = &event.ID
		nextCase, nextContact := c, contact
		if err := tx.Transaction(func(inner *gorm.DB) error {
			return a.runInboxWorkflowActions(inner, &nextCase, &nextContact, user, w, now)
		}); err != nil {
			switch {
			case errors.Is(err, errInboxTransition), errors.Is(err, gorm.ErrRecordNotFound):
				run.State, run.Outcome = inboxRunFailed, "action_rejected"
			default:
				return err
			}
		} else {
			c, contact = nextCase, nextContact
			switch {
			case preview.SendUnavailable:
				run.Outcome = "send_ineligible"
			case preview.Digest == "":
			case sendSelected:
				run.Outcome = "event_send_already_selected"
			case !org.InboxAutomationSendsEnabled || !a.inboxDispatchAllowed():
				run.Outcome = "sending_disabled"
			default:
				run.State, run.SendDigest = inboxRunPending, preview.Digest
				sendSelected = true
			}
		}
		if err := saveInboxWorkflowRun(tx, c, &run); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) inboxDispatchAllowed() bool {
	return a.Config != nil && a.Config.InboxAutomation.DispatchEnabled
}

func (a *App) claimInboxSend(ctx context.Context, id, orgID uuid.UUID, now time.Time) (models.InboxWorkflowRun, bool, error) {
	var run models.InboxWorkflowRun
	claimed := false
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var org models.Organization
		err := tx.Select("inbox_workflows_enabled", "inbox_automation_sends_enabled").
			Clauses(clause.Locking{Strength: "SHARE"}).First(&org, "id = ?", orgID).Error
		if err != nil {
			return err
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&run, "id = ? AND organization_id = ? AND state = ?", id, orgID, inboxRunPending).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !org.InboxWorkflowsEnabled || (run.Kind == "rule" && !org.InboxAutomationSendsEnabled) {
			return finishInboxSend(tx, run, inboxRunBlocked, "sending_disabled")
		}
		if err := tx.Model(&run).Updates(map[string]any{"state": inboxRunSubmitting, "submission_started_at": now}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return run, claimed, err
}

func finishInboxSend(tx *gorm.DB, run models.InboxWorkflowRun, state, outcome string) error {
	res := tx.Model(&models.InboxWorkflowRun{}).Where("id = ? AND organization_id = ? AND state IN ?", run.ID, run.OrganizationID, []string{inboxRunPending, inboxRunSubmitting, inboxRunUnknown}).
		Updates(map[string]any{"state": state, "outcome": outcome})
	if res.Error != nil || res.RowsAffected == 0 {
		return res.Error
	}
	c := models.InboxCase{BaseModel: models.BaseModel{ID: run.CaseID}, OrganizationID: run.OrganizationID, AccountID: run.AccountID}
	return appendInboxActivity(tx, c, models.InboxCaseActivity{Kind: "workflow_" + state, ActorID: &run.ActorID, Detail: run.WorkflowName})
}

func (a *App) dispatchClaimedInboxSend(ctx context.Context, run models.InboxWorkflowRun) (string, string) {
	var w models.InboxWorkflow
	if err := a.DB.First(&w, "id = ? AND organization_id = ?", run.WorkflowID, run.OrganizationID).Error; err != nil ||
		!w.UpdatedAt.Equal(run.WorkflowUpdatedAt) || (w.Kind == "rule" && w.EnabledAt == nil) {
		return inboxRunBlocked, "workflow_changed"
	}
	if !a.workflowActorActive(a.DB, run.OrganizationID, run.ActorID) ||
		!a.HasPermission(run.ActorID, models.ResourceChat, models.ActionWrite, run.OrganizationID) ||
		(w.Kind == "rule" && !a.HasPermission(run.ActorID, models.ResourceSettingsGeneral, models.ActionWrite, run.OrganizationID)) {
		return inboxRunBlocked, "actor_unavailable"
	}
	var org models.Organization
	if a.DB.Select("inbox_workflows_enabled", "inbox_automation_sends_enabled").First(&org, "id = ?", run.OrganizationID).Error != nil ||
		!org.InboxWorkflowsEnabled || (w.Kind == "rule" && !org.InboxAutomationSendsEnabled) || !a.inboxDispatchAllowed() {
		return inboxRunBlocked, "sending_disabled"
	}
	thread := inboxThread{run.OrganizationID, run.AccountID, run.ContactID}
	c, err := thread.latest(a.DB)
	if err != nil || c.ID != run.CaseID {
		return inboxRunBlocked, "case_changed"
	}
	var scoped models.InboxCase
	if err := a.scopeInboxCases(a.DB.Model(&scoped), run.ActorID, run.OrganizationID).First(&scoped, "inbox_cases.id = ?", c.ID).Error; err != nil {
		return inboxRunBlocked, "case_access_changed"
	}
	account, contact, err := a.inboxWorkflowContext(a.DB, c)
	if err != nil {
		return inboxRunBlocked, "conversation_unavailable"
	}
	a.decryptAccountSecrets(&account)
	if account.AccessToken == "" || crypto.IsEncrypted(account.AccessToken) {
		return inboxRunBlocked, "account_unavailable"
	}
	var action models.InboxAction
	for _, item := range w.Actions {
		if item.Type == "send_text" || item.Type == "send_template" {
			action = item
			break
		}
	}
	preview, err := previewInboxReplyWithDB(a.DB, c, account, contact, action, time.Now().UTC())
	if err != nil || inboxReplyFingerprint(c, account, contact, preview) != run.SendDigest {
		return inboxRunBlocked, "send_ineligible"
	}
	req := OutgoingMessageRequest{IdempotencyKey: run.ID, Account: &account, Contact: &contact, Type: models.MessageTypeText, Content: preview.Text}
	if action.Type == "send_template" {
		var template models.Template
		if err := a.DB.First(&template, "id = ? AND organization_id = ? AND whats_app_account = ? AND status = 'APPROVED' AND category = 'UTILITY'", action.Value, run.OrganizationID, account.Name).Error; err != nil || template.Name != preview.TemplateKey || template.BodyContent != preview.Text || template.HeaderContent != preview.Header || template.FooterContent != preview.Footer || template.Language != preview.Language || (template.HeaderType != "" && template.HeaderType != "TEXT") || len(template.Buttons) != 0 {
			return inboxRunBlocked, "send_ineligible"
		}
		req.Type, req.Template = models.MessageTypeTemplate, &template
	}
	opts := DefaultSendOptions()
	opts.Async = false
	if w.Kind == "macro" {
		opts.SentByUserID = &run.ActorID
	}
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msg, err := a.SendOutgoingMessage(timeout, req, opts)
	if err != nil || msg == nil || !a.inboxSendReceipt(run) {
		return inboxRunUnknown, "needs_reconciliation"
	}
	return inboxRunSent, ""
}

func (a *App) inboxSendReceipt(run models.InboxWorkflowRun) bool {
	var count int64
	err := a.DB.Model(&models.Message{}).Where("id = ? AND organization_id = ? AND contact_id = ? AND whats_app_message_id <> '' AND status IN ?",
		run.ID, run.OrganizationID, run.ContactID, []models.MessageStatus{models.MessageStatusSent, models.MessageStatusDelivered, models.MessageStatusRead}).Count(&count).Error
	return err == nil && count == 1
}

func (a *App) reconcileInboxSends(ctx context.Context, now time.Time) error {
	var stale []models.InboxWorkflowRun
	if err := a.DB.WithContext(ctx).
		Where("(state = ? AND submission_started_at < ?) OR (state = ? AND EXISTS (SELECT 1 FROM messages m WHERE m.id = inbox_workflow_runs.id AND m.organization_id = inbox_workflow_runs.organization_id AND m.whats_app_message_id <> ''))", inboxRunSubmitting, now.Add(-2*time.Minute), inboxRunUnknown).
		Order("updated_at, id").Limit(50).Find(&stale).Error; err != nil {
		return err
	}
	for _, run := range stale {
		var msg models.Message
		err := a.DB.Where("id = ? AND organization_id = ? AND contact_id = ? AND whats_app_message_id <> '' AND status IN ?",
			run.ID, run.OrganizationID, run.ContactID, []models.MessageStatus{models.MessageStatusSent, models.MessageStatusDelivered, models.MessageStatusRead}).First(&msg).Error
		state, outcome := inboxRunSent, "reconciled"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if run.State == inboxRunUnknown {
				continue
			}
			state, outcome = inboxRunUnknown, "needs_reconciliation"
		} else if err != nil {
			return err
		}
		if err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return finishInboxSend(tx, run, state, outcome) }); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runInboxWorkflowCycle(ctx context.Context) error {
	var events []models.InboxCaseActivity
	if err := a.DB.WithContext(ctx).
		Where("workflow_processed_at IS NULL AND workflow_trigger <> ''").
		Where("EXISTS (SELECT 1 FROM organizations o WHERE o.id = inbox_case_activities.organization_id AND o.inbox_workflows_enabled AND o.deleted_at IS NULL)").
		Order("created_at, id").Limit(50).Find(&events).Error; err != nil {
		return err
	}
	for _, event := range events {
		if err := a.processInboxRuleEvent(ctx, event); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	if err := a.reconcileInboxSends(ctx, now); err != nil {
		return err
	}
	if !a.inboxDispatchAllowed() {
		return nil
	}
	var pending []models.InboxWorkflowRun
	if err := a.DB.WithContext(ctx).Select("id", "organization_id").Where("state = ?", inboxRunPending).Order("created_at, id").Limit(10).Find(&pending).Error; err != nil {
		return err
	}
	for _, item := range pending {
		run, claimed, err := a.claimInboxSend(ctx, item.ID, item.OrganizationID, now)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		state, outcome := a.dispatchClaimedInboxSend(ctx, run)
		if err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return finishInboxSend(tx, run, state, outcome) }); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) RunInboxWorkflows(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := a.runInboxWorkflowCycle(ctx); err != nil && ctx.Err() == nil {
			a.Log.Error("Inbox workflow cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
