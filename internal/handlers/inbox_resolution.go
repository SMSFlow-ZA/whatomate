package handlers

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxMissingField struct {
	models.InboxFieldDefinition
	CanEdit             bool   `json:"can_edit"`
	RequiredNow         bool   `json:"required_now"`
	ConditionalRequired bool   `json:"conditional_required"`
	EditorPath          string `json:"editor_path,omitempty"`
	Handoff             string `json:"handoff,omitempty"`
}

func (a *App) missingInboxFields(db *gorm.DB, c models.InboxCase, user uuid.UUID) ([]inboxMissingField, error) {
	definitions, err := inboxMissingDefinitions(db, c)
	if err != nil {
		return nil, err
	}
	result := make([]inboxMissingField, 0, len(definitions))
	for _, d := range definitions {
		editable := a.HasPermission(user, models.ResourceChat, models.ActionWrite, c.OrganizationID)
		if d.Scope == "contact" {
			editable = editable && a.HasPermission(user, models.ResourceContacts, models.ActionRead, c.OrganizationID) && a.HasPermission(user, models.ResourceContacts, models.ActionWrite, c.OrganizationID)
		}
		field := inboxMissingField{InboxFieldDefinition: d, CanEdit: editable, RequiredNow: true, ConditionalRequired: d.RequiredWhen != nil}
		if !editable {
			field.Handoff = "Ask a supervisor with contact editing access to complete this detail, then retry resolution."
			if a.HasPermission(user, models.ResourceContacts, models.ActionRead, c.OrganizationID) {
				field.EditorPath = "/settings/contacts/" + c.ContactID.String()
			}
		}
		result = append(result, field)
	}
	return result, nil
}

func (a *App) InboxResolutionRequirements(r *fastglue.Request) error {
	t, user, err := a.inboxRequest(r, models.ActionRead)
	if err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, t.org, false)
	if err != nil {
		return a.inboxError(r, err)
	}
	if !enabled {
		return a.inboxError(r, errInboxDisabled)
	}
	contact, err := a.inboxThreadAccess(a.DB, t, user)
	if err != nil {
		return a.inboxError(r, err)
	}
	c, err := t.latest(a.DB)
	if err != nil {
		return a.inboxError(r, err)
	}
	if err := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, t.org).First(&models.InboxCase{}, "inbox_cases.id=?", c.ID).Error; err != nil {
		return a.inboxError(r, err)
	}
	missing, err := a.missingInboxFields(a.DB, c, user)
	if err != nil {
		return a.inboxError(r, err)
	}
	var account models.WhatsAppAccount
	if err := a.DB.Select("name").First(&account, "id=? AND organization_id=?", t.account, t.org).Error; err != nil {
		return a.inboxError(r, err)
	}
	name := contact.ProfileName
	if a.ShouldMaskPhoneNumbers(t.org) {
		name = utils.MaskIfPhoneNumber(name)
	}
	return r.SendEnvelope(map[string]any{"case_id": c.ID, "version": c.Version, "status": c.Status, "missing_fields": missing, "contact_name": name, "account_name": account.Name})
}

type inboxResolutionFieldError struct {
	DefinitionID uuid.UUID `json:"definition_id"`
	Label        string    `json:"label"`
	Code         string    `json:"code"`
}
type inboxResolutionProblem struct {
	Code   string
	Fields []inboxResolutionFieldError
}

func (e inboxResolutionProblem) Error() string {
	return "Review the highlighted details before resolving"
}

// Values and the resolution transition commit together. A stale case, changed
// definition, missing permission or invalid value rolls back the entire form.
func (a *App) CompleteAndResolveInboxCase(r *fastglue.Request) error {
	t, user, err := a.inboxRequest(r, models.ActionWrite)
	if err != nil {
		return nil
	}
	var input struct {
		CaseID  uuid.UUID `json:"case_id"`
		Version int64     `json:"version"`
		Values  []struct {
			DefinitionID uuid.UUID       `json:"definition_id"`
			Value        json.RawMessage `json:"value"`
		} `json:"values"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if len(input.Values) > 100 {
		return a.inboxError(r, errInboxTransition)
	}
	var c models.InboxCase
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		enabled, e := inboxWorkflowEnabled(tx, t.org, true)
		if e != nil {
			return e
		}
		if !enabled {
			return errInboxDisabled
		}
		if e := t.lock(tx); e != nil {
			return e
		}
		if _, e := a.inboxThreadAccess(tx, t, user); e != nil {
			return e
		}
		c, e = t.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if e != nil {
			return e
		}
		if c.ID != input.CaseID || c.Version != input.Version || c.Status == models.InboxCaseResolved {
			return errInboxConflict
		}
		if e := a.scopeInboxCases(tx.Model(&models.InboxCase{}), user, t.org).First(&models.InboxCase{}, "inbox_cases.id=?", c.ID).Error; e != nil {
			return e
		}
		seen := map[uuid.UUID]bool{}
		for _, item := range input.Values {
			if seen[item.DefinitionID] || len(item.Value) == 0 || len(item.Value) > 32768 {
				return errInboxTransition
			}
			seen[item.DefinitionID] = true
			var d models.InboxFieldDefinition
			if e := tx.First(&d, "organization_id=? AND id=? AND archived_at IS NULL", t.org, item.DefinitionID).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return inboxResolutionProblem{Code: "requirements_changed"}
				}
				return e
			}
			if d.Scope == "contact" && (!a.HasPermission(user, models.ResourceContacts, models.ActionRead, t.org) || !a.HasPermission(user, models.ResourceContacts, models.ActionWrite, t.org)) {
				return inboxResolutionProblem{Code: "permission_required", Fields: []inboxResolutionFieldError{{DefinitionID: d.ID, Label: d.Label, Code: "contact_edit_required"}}}
			}
			if d.ValidateValue(item.Value) != nil {
				return inboxResolutionProblem{Code: "invalid_values", Fields: []inboxResolutionFieldError{{DefinitionID: d.ID, Label: d.Label, Code: "invalid_value"}}}
			}
			subject := c.ID
			if d.Scope == "contact" {
				subject = c.ContactID
			}
			value := models.InboxFieldValue{OrganizationID: t.org, SubjectID: subject, DefinitionID: d.ID, Value: item.Value}
			if emptyInboxField(item.Value) {
				if e := tx.Delete(&value).Error; e != nil {
					return e
				}
			} else if e := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&value).Error; e != nil {
				return e
			}
		}
		return applyInboxTransition(tx, &c, inboxCaseUpdate{Status: models.InboxCaseResolved}, &user, time.Now().UTC(), "updated")
	})
	var problem inboxResolutionProblem
	if errors.As(err, &problem) {
		status := fasthttp.StatusBadRequest
		if problem.Code == "permission_required" {
			status = fasthttp.StatusForbidden
		}
		if problem.Code == "requirements_changed" {
			status = fasthttp.StatusConflict
		}
		return r.SendErrorEnvelope(status, problem.Error(), map[string]any{"code": problem.Code, "field_errors": problem.Fields}, "inbox_resolution")
	}
	if errors.Is(err, errRequiredInboxFields) {
		missing, e := a.missingInboxFields(a.DB, c, user)
		if e != nil {
			return a.inboxError(r, e)
		}
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, err.Error(), map[string]any{"missing_fields": missing, "code": "required_fields"}, "inbox_resolution")
	}
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"case": c})
}
