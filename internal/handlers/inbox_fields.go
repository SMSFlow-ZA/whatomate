package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errRequiredInboxFields = errors.New("complete the required fields before resolving")

func (a *App) ListInboxFieldDefinitions(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	scope := string(r.RequestCtx.QueryArgs().Peek("scope"))
	q := a.DB.Where("organization_id = ?", org)
	switch scope {
	case "", "case", "contact":
		if scope != "" {
			q = q.Where("scope = ?", scope)
		}
	default:
		return a.inboxError(r, errInboxTransition)
	}
	if !a.HasPermission(user, models.ResourceContacts, models.ActionRead, org) {
		q = q.Where("scope = 'case'")
	}
	definitions := []models.InboxFieldDefinition{}
	if err := q.Order("archived_at NULLS FIRST, label, id").Find(&definitions).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"definitions": definitions})
}

func (a *App) CreateInboxFieldDefinition(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var input models.InboxFieldDefinition
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	d := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()},
		OrganizationID: org, Scope: input.Scope, Key: input.Key, Label: strings.TrimSpace(input.Label),
		Kind: input.Kind, Options: input.Options, RequiredOnResolve: input.RequiredOnResolve}
	if err := d.Validate(); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockInboxFieldDefinitions(tx, org); err != nil {
			return err
		}
		var existing int64
		if err := tx.Model(&models.InboxFieldDefinition{}).Where("organization_id = ? AND scope = ? AND key = ?", org, d.Scope, d.Key).
			Count(&existing).Error; err != nil {
			return err
		}
		if existing != 0 {
			return errInboxConflict
		}
		return tx.Create(&d).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"definition": d})
}

func lockInboxFieldDefinitions(tx *gorm.DB, org uuid.UUID) error {
	var organization models.Organization
	return tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).First(&organization, "id = ?", org).Error
}

func (a *App) UpdateInboxFieldDefinition(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "definition_id", "Field definition")
	if err != nil {
		return nil
	}
	var input struct {
		Label             *string `json:"label"`
		RequiredOnResolve *bool   `json:"required_on_resolve"`
		Archive           bool    `json:"archive"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var d models.InboxFieldDefinition
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockInboxFieldDefinitions(tx, org); err != nil {
			return err
		}
		if err := tx.First(&d, "id = ? AND organization_id = ?", id, org).Error; err != nil {
			return err
		}
		if d.ArchivedAt != nil {
			return errInboxConflict
		}
		if input.Archive {
			now := time.Now().UTC()
			d.ArchivedAt = &now
		}
		if input.Label != nil {
			d.Label = strings.TrimSpace(*input.Label)
		}
		if input.RequiredOnResolve != nil {
			d.RequiredOnResolve = *input.RequiredOnResolve
		}
		if err := d.Validate(); err != nil {
			return errInboxTransition
		}
		return tx.Save(&d).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"definition": d})
}

type inboxFieldSubject struct {
	org, user, account, contact, id uuid.UUID
	scope                           string
}

func (a *App) fieldSubject(r *fastglue.Request, action string, isCase bool) (inboxFieldSubject, error) {
	var s inboxFieldSubject
	org, user, ok := a.inboxFeatureAuth(r, action)
	if !ok {
		return s, errInboxDisabled
	}
	contact, err := parsePathUUID(r, "id", "Contact")
	if err != nil {
		return s, err
	}
	s = inboxFieldSubject{org: org, user: user, contact: contact, id: contact, scope: "contact"}
	if isCase {
		s.scope = "case"
		s.account, err = parsePathUUID(r, "account_id", "WhatsApp account")
	} else {
		_, _, err = a.requireAuth(r, models.ResourceContacts, action)
	}
	return s, err
}

func (a *App) accessInboxFieldSubject(db *gorm.DB, subject inboxFieldSubject) (uuid.UUID, error) {
	if subject.scope == "contact" {
		var contact models.Contact
		if err := db.Select("id").First(&contact, "organization_id = ? AND id = ?", subject.org, subject.contact).Error; err != nil {
			return uuid.Nil, err
		}
		return contact.ID, nil
	}
	thread := inboxThread{subject.org, subject.account, subject.contact}
	c, err := thread.latest(db)
	if err != nil {
		return uuid.Nil, err
	}
	var checked models.InboxCase
	if err := a.scopeInboxCases(db.Model(&checked), subject.user, subject.org).First(&checked, "inbox_cases.id = ?", c.ID).Error; err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

func (a *App) readInboxFieldValues(r *fastglue.Request, isCase bool) error {
	s, err := a.fieldSubject(r, models.ActionRead, isCase)
	if err != nil {
		return nil
	}
	id, err := a.accessInboxFieldSubject(a.DB, s)
	if err != nil {
		return a.inboxError(r, err)
	}
	values := []models.InboxFieldValue{}
	defs := a.DB.Model(&models.InboxFieldDefinition{}).Select("id").Where("organization_id = ? AND scope = ?", s.org, s.scope)
	if err := a.DB.Where("organization_id = ? AND subject_id = ? AND definition_id IN (?)", s.org, id, defs).Find(&values).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"values": values, "subject_id": id})
}

func (a *App) writeInboxFieldValue(r *fastglue.Request, isCase bool) error {
	s, err := a.fieldSubject(r, models.ActionWrite, isCase)
	if err != nil {
		return nil
	}
	definition, err := parsePathUUID(r, "definition_id", "Field definition")
	if err != nil {
		return nil
	}
	var input struct {
		Value     json.RawMessage `json:"value"`
		SubjectID uuid.UUID       `json:"subject_id"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if len(input.Value) == 0 || len(input.Value) > 32768 {
		return a.inboxError(r, errInboxTransition)
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, s.org, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxDisabled
		}
		if err := (inboxThread{org: s.org, contact: s.contact}).lock(tx); err != nil {
			return err
		}
		id, err := a.accessInboxFieldSubject(tx, s)
		if err != nil {
			return err
		}
		if input.SubjectID != id {
			return errInboxConflict
		}
		var d models.InboxFieldDefinition
		if err := tx.First(&d, "id = ? AND organization_id = ? AND scope = ? AND archived_at IS NULL", definition, s.org, s.scope).Error; err != nil {
			return err
		}
		if d.ValidateValue(input.Value) != nil {
			return errInboxTransition
		}
		value := models.InboxFieldValue{OrganizationID: s.org, SubjectID: id, DefinitionID: d.ID, Value: input.Value}
		if emptyInboxField(input.Value) {
			return tx.Delete(&value).Error
		}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&value).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"saved": true})
}

func (a *App) ListInboxCaseFields(r *fastglue.Request) error { return a.readInboxFieldValues(r, true) }
func (a *App) ListInboxContactFields(r *fastglue.Request) error {
	return a.readInboxFieldValues(r, false)
}
func (a *App) SetInboxCaseField(r *fastglue.Request) error { return a.writeInboxFieldValue(r, true) }
func (a *App) SetInboxContactField(r *fastglue.Request) error {
	return a.writeInboxFieldValue(r, false)
}

func emptyInboxField(value json.RawMessage) bool {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return true
	}
	var text string
	return json.Unmarshal(value, &text) == nil && strings.TrimSpace(text) == ""
}

func checkInboxRequiredFields(tx *gorm.DB, c models.InboxCase) error {
	var missing int64
	err := tx.Model(&models.InboxFieldDefinition{}).
		Where("organization_id = ? AND required_on_resolve AND archived_at IS NULL", c.OrganizationID).
		Where("NOT EXISTS (SELECT 1 FROM inbox_field_values v WHERE v.organization_id = inbox_field_definitions.organization_id "+
			"AND v.definition_id = inbox_field_definitions.id AND v.subject_id = CASE WHEN inbox_field_definitions.scope = 'case' THEN ?::uuid ELSE ?::uuid END)",
			c.ID, c.ContactID).
		Count(&missing).Error
	if err != nil {
		return err
	}
	if missing != 0 {
		return errRequiredInboxFields
	}
	return nil
}

func queryInboxFieldMatchers(q *gorm.DB, org uuid.UUID, matches []models.InboxFieldMatch, allowContact bool) (*gorm.DB, error) {
	for _, match := range matches {
		if match.DefinitionID == uuid.Nil || !json.Valid(match.Value) || len(match.Value) > 32768 {
			return nil, errInboxTransition
		}
		q = q.Where("EXISTS (SELECT 1 FROM inbox_field_definitions d WHERE d.organization_id = ? AND d.id = ? AND d.deleted_at IS NULL "+
			"AND (d.scope = 'case' OR ?) AND EXISTS (SELECT 1 FROM inbox_field_values v WHERE v.organization_id = d.organization_id "+
			"AND v.definition_id = d.id AND v.subject_id = CASE WHEN d.scope = 'case' THEN inbox_cases.id ELSE inbox_cases.contact_id END "+
			"AND v.value = ?::jsonb))", org, match.DefinitionID, allowContact, string(bytes.TrimSpace(match.Value)))
	}
	return q, nil
}

func validateInboxFieldMatchers(db *gorm.DB, org uuid.UUID, matches []models.InboxFieldMatch, allowContact bool) error {
	for _, match := range matches {
		var d models.InboxFieldDefinition
		if err := db.First(&d, "id = ? AND organization_id = ? AND (scope = 'case' OR ?)", match.DefinitionID, org, allowContact).Error; err != nil {
			return err
		}
		if err := d.ValidateValue(match.Value); err != nil || emptyInboxField(match.Value) {
			return fmt.Errorf("%w: invalid field match", errInboxTransition)
		}
	}
	return nil
}
