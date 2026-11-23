package handlers

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const inboxWorkflowSettingsResource = models.ResourceSettingsGeneral

func validInboxWorkflow(w models.InboxWorkflow) bool {
	if strings.TrimSpace(w.Name) == "" || utf8.RuneCountInString(w.Name) > 100 || utf8.RuneCountInString(w.Description) > 1000 || len(w.Actions) == 0 || len(w.Actions) > 10 || w.Position < -10000 || w.Position > 10000 {
		return false
	}
	switch w.Kind {
	case "macro":
		if w.Trigger != "" || w.Conditions.MessageContains != "" || (w.Visibility != "" && w.Visibility != "organization" && w.Visibility != "personal" && w.Visibility != "team") || (w.Visibility == "team" && w.TeamID == nil) || (w.Visibility != "team" && w.TeamID != nil) {
			return false
		}
	case "rule":
		switch w.Trigger {
		case "case_opened", "incoming_message", "status_changed":
		default:
			return false
		}
	default:
		return false
	}
	if w.Conditions.Search != "" || w.Conditions.Queue != "" || !validateInboxFilter(w.Conditions) {
		return false
	}
	sends := 0
	for _, action := range w.Actions {
		switch action.Type {
		case "snooze":
			if action.DurationMinutes < 1 || action.DurationMinutes > 43200 || action.Value != "" {
				return false
			}
		case "assign", "assign_team", "send_template":
			if action.Type == "send_template" && !action.ConsentConfirmed {
				return false
			}
			if id, err := uuid.Parse(action.Value); err != nil || id == uuid.Nil {
				return false
			}
		case "tag":
			if strings.TrimSpace(action.Value) == "" || utf8.RuneCountInString(action.Value) > 100 {
				return false
			}
		case "note", "send_text":
			if strings.TrimSpace(action.Value) == "" || utf8.RuneCountInString(action.Value) > 4096 {
				return false
			}
		case "priority":
			if action.Value == "" || !(inboxCaseUpdate{Priority: models.InboxCasePriority(action.Value)}).valid(time.Time{}) {
				return false
			}
		case "status":
			if action.Value == "" || !(inboxCaseUpdate{Status: models.InboxCaseStatus(action.Value)}).valid(time.Time{}) {
				return false
			}
		default:
			return false
		}
		if action.Type == "send_template" || action.Type == "send_text" {
			sends++
		}
		if sends > 1 {
			return false
		}
	}
	return true
}

func (a *App) ListInboxWorkflows(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	kind := string(r.RequestCtx.QueryArgs().Peek("kind"))
	if kind != "macro" && kind != "rule" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Workflow kind is required", nil, "")
	}
	if kind == "rule" {
		if _, _, err := a.requireAuth(r, inboxWorkflowSettingsResource, models.ActionWrite); err != nil {
			return nil
		}
	}
	workflows := []models.InboxWorkflow{}
	if err := a.visibleInboxWorkflows(a.DB, org, user).Where("kind = ?", kind).
		Order("position, created_at, id").Find(&workflows).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"workflows": workflows})
}

type inboxWorkflowRequest struct {
	Description string                  `json:"description"`
	Visibility  string                  `json:"visibility"`
	TeamID      *uuid.UUID              `json:"team_id"`
	Enabled     bool                    `json:"enabled"`
	Name        string                  `json:"name"`
	Kind        string                  `json:"kind"`
	Trigger     string                  `json:"trigger"`
	Position    int                     `json:"position"`
	Conditions  models.InboxQueueFilter `json:"conditions"`
	Actions     []models.InboxAction    `json:"actions"`
}

func (a *App) CreateInboxWorkflow(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	if _, _, err := a.requireAuth(r, inboxWorkflowSettingsResource, models.ActionWrite); err != nil {
		return nil
	}
	var input inboxWorkflowRequest
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	w := models.InboxWorkflow{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, CreatedByID: user,
		Name: strings.TrimSpace(input.Name), Kind: input.Kind, Trigger: input.Trigger,
		Position: input.Position, Conditions: input.Conditions, Actions: input.Actions,
		Description: strings.TrimSpace(input.Description), Visibility: input.Visibility, TeamID: input.TeamID,
	}
	if w.Visibility == "" {
		w.Visibility = "organization"
	}
	if !a.validInboxWorkflowTeam(a.DB, org, w) {
		return a.inboxError(r, errInboxTransition)
	}
	if !validInboxWorkflow(w) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid inbox workflow", nil, "")
	}
	if err := validateInboxFieldMatchers(a.DB, org, w.Conditions.Fields, a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)); err != nil {
		return a.inboxError(r, errInboxTransition)
	}
	if err := validateInboxPredicates(a.DB, org, w.Conditions, a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)); err != nil {
		return a.inboxError(r, err)
	}
	if err := a.DB.Create(&w).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"workflow": w})
}

func (a *App) DeleteInboxWorkflow(r *fastglue.Request) error {
	org, _, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	if _, _, err := a.requireAuth(r, inboxWorkflowSettingsResource, models.ActionWrite); err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "inbox workflow")
	if err != nil {
		return nil
	}
	res := a.DB.Where("organization_id = ? AND id = ?", org, id).Delete(&models.InboxWorkflow{})
	if res.Error != nil {
		return a.inboxError(r, res.Error)
	}
	if res.RowsAffected != 1 {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Workflow not found", nil, "")
	}
	return r.SendEnvelope(map[string]bool{"deleted": true})
}

func (a *App) UpdateInboxWorkflow(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionWrite)
	if !ok {
		return nil
	}
	if _, _, err := a.requireAuth(r, inboxWorkflowSettingsResource, models.ActionWrite); err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "inbox workflow")
	if err != nil {
		return nil
	}
	var input inboxWorkflowRequest
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var w models.InboxWorkflow
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, "id = ? AND organization_id = ?", id, org).Error; err != nil {
			return err
		}
		w.Name, w.Trigger, w.Position = strings.TrimSpace(input.Name), input.Trigger, input.Position
		w.Actions, w.Conditions = input.Actions, input.Conditions
		w.Description, w.Visibility, w.TeamID = strings.TrimSpace(input.Description), input.Visibility, input.TeamID
		if w.Visibility == "" {
			w.Visibility = "organization"
		}
		// Rules execute as their last editor, never with an earlier author's rights.
		w.UpdatedByID = &user
		if input.Kind != w.Kind || !validInboxWorkflow(w) || !a.validInboxWorkflowTeam(tx, org, w) {
			return errInboxTransition
		}
		if err := validateInboxFieldMatchers(tx, org, w.Conditions.Fields, a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)); err != nil {
			return errInboxTransition
		}
		if err := validateInboxPredicates(tx, org, w.Conditions, a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)); err != nil {
			return err
		}
		w.EnabledAt = nil
		if w.Kind == "rule" && input.Enabled {
			now := time.Now().UTC()
			w.EnabledAt = &now
		}
		return tx.Save(&w).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"workflow": w})
}

func (a *App) InboxAutomationState(r *fastglue.Request) error {
	org, _, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	var settings models.Organization
	if err := a.DB.Select("inbox_automation_sends_enabled").First(&settings, "id = ?", org).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"enabled": settings.InboxAutomationSendsEnabled, "dispatch_available": a.inboxDispatchAllowed()})
}

func (a *App) SetInboxAutomationState(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, inboxWorkflowSettingsResource, models.ActionWrite)
	if err != nil {
		return nil
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if input.Enabled == nil || (*input.Enabled && !a.inboxDispatchAllowed()) {
		return a.inboxError(r, errInboxTransition)
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Organization{}).Where("id = ?", org).Update("inbox_automation_sends_enabled", *input.Enabled).Error; err != nil {
			return err
		}
		if *input.Enabled {
			return nil
		}
		var runs []models.InboxWorkflowRun
		if err := tx.Where("organization_id = ? AND kind = 'rule' AND state = ?", org, inboxRunPending).Find(&runs).Error; err != nil {
			return err
		}
		for _, run := range runs {
			if err := finishInboxSend(tx, run, inboxRunBlocked, "sending_disabled"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"enabled": *input.Enabled, "dispatch_available": a.inboxDispatchAllowed()})
}
