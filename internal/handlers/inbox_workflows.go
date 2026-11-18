package handlers

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const inboxWorkflowSettingsResource = models.ResourceSettingsGeneral

func validInboxWorkflow(w models.InboxWorkflow) bool {
	if strings.TrimSpace(w.Name) == "" || utf8.RuneCountInString(w.Name) > 100 || len(w.Actions) == 0 || len(w.Actions) > 10 {
		return false
	}
	switch w.Kind {
	case "macro":
		if w.Trigger != "" {
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
	if w.Conditions.Queue != "" || !validateInboxFilter(w.Conditions) {
		return false
	}
	sends := 0
	for _, action := range w.Actions {
		switch action.Type {
		case "assign", "send_template":
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
	org, _, ok := a.inboxFeatureAuth(r, models.ActionRead)
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
	if err := a.DB.Where("organization_id = ? AND kind = ?", org, kind).
		Order("position, created_at, id").Find(&workflows).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"workflows": workflows})
}

type inboxWorkflowRequest struct {
	Name       string                  `json:"name"`
	Kind       string                  `json:"kind"`
	Trigger    string                  `json:"trigger"`
	Position   int                     `json:"position"`
	Conditions models.InboxQueueFilter `json:"conditions"`
	Actions    []models.InboxAction    `json:"actions"`
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
	}
	if !validInboxWorkflow(w) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid inbox workflow", nil, "")
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
