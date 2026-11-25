package handlers

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

func graphUsesIntake(raw models.JSONB) bool {
	if raw == nil {
		return false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	var partial struct {
		Nodes []struct {
			Config map[string]any `json:"config"`
		} `json:"nodes"`
	}
	if json.Unmarshal(encoded, &partial) != nil {
		return false
	}
	for _, node := range partial.Nodes {
		if node.Config["intake_mapping_id"] != nil && node.Config["intake_mapping_id"] != "" {
			return true
		}
	}
	return false
}

// Inspect only the optional intake configuration. Legacy graphs keep their
// existing validation/upgrade behaviour when no mapping is selected.
func validateChatIntakeGraph(tx *gorm.DB, org, user uuid.UUID, channel string, raw models.JSONB) error {
	if !graphUsesIntake(raw) {
		return nil
	}
	if channel != "whatsapp" {
		return inboxIntakeProblem{"whatsapp_prompt_required"}
	}
	if err := intakeAuthority(tx, org, user); err != nil {
		return err
	}
	graph, err := parseChatGraph(raw)
	if err != nil || graph == nil || len(graph.Nodes) > 100 {
		return inboxIntakeProblem{"invalid_mapping"}
	}
	for _, node := range graph.Nodes {
		rawID, exists := node.Config["intake_mapping_id"]
		if !exists || rawID == nil || rawID == "" {
			continue
		}
		id, err := uuid.Parse(stringFromConfig(node.Config, "intake_mapping_id"))
		if err != nil || id == uuid.Nil {
			return inboxIntakeProblem{"invalid_mapping"}
		}
		if node.Type != ChatNodePrompt && node.Type != ChatNodeButtons && node.Type != ChatNodeWhatsAppFlow {
			return inboxIntakeProblem{"unsupported_step"}
		}
		if len(node.ID) > 100 {
			return inboxIntakeProblem{"invalid_mapping"}
		}
		if rawDelivery, exists := node.Config["delivery_options"]; exists && rawDelivery != nil {
			var delivery struct {
				Channel  string `json:"channel"`
				Fallback bool   `json:"fallback_enabled"`
			}
			encoded, err := json.Marshal(rawDelivery)
			if err != nil || json.Unmarshal(encoded, &delivery) != nil || (delivery.Channel != "" && delivery.Channel != "whatsapp") || delivery.Fallback {
				return inboxIntakeProblem{"whatsapp_prompt_required"}
			}
		}
		for _, button := range buttonsFromConfig(node.Config) {
			if kind, _ := button["type"].(string); kind != "" && kind != "reply" {
				return inboxIntakeProblem{"whatsapp_prompt_required"}
			}
		}
		var mapping models.InboxIntakeMapping
		if err := tx.First(&mapping, "id=? AND organization_id=?", id, org).Error; err != nil {
			return err
		}
		if err := validateIntakeMapping(tx, mapping); err != nil {
			return err
		}
		if node.Type != ChatNodeWhatsAppFlow {
			if mapping.ConsentKey == "answer" {
				return inboxIntakeProblem{"prompt_answer_required"}
			}
			for _, input := range mapping.Fields {
				if input.Input != "answer" {
					return inboxIntakeProblem{"prompt_answer_required"}
				}
			}
		}
	}
	return nil
}

func (a *App) intakeError(r *fastglue.Request, err error) error {
	var problem inboxIntakeProblem
	if errors.As(err, &problem) {
		status := 422
		if problem.Reason == "permission_required" {
			status = 403
		}
		if problem.Reason == "revision_changed" {
			status = 409
		}
		if problem.Reason == "invalid_mapping" {
			status = 400
		}
		return r.SendErrorEnvelope(status, "Review the intake mapping and try again", map[string]string{"reason": problem.Reason}, "intake_unavailable")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.SendErrorEnvelope(404, "Intake mapping not found", nil, "")
	}
	if errors.Is(err, errInboxDisabled) {
		return a.inboxError(r, err)
	}
	a.Log.Error("Intake operation failed", "code", audit.ErrorCode(err))
	return r.SendErrorEnvelope(503, "Intake is temporarily unavailable; try again", nil, "intake_unavailable")
}

func (a *App) intakeRequest(r *fastglue.Request) (uuid.UUID, uuid.UUID, bool) {
	org, user, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return org, user, false
	}
	claims, _ := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims)
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	if err := intakeAuthority(db, org, user, claims); err != nil {
		_ = a.intakeError(r, err)
		return org, user, false
	}
	return org, user, true
}

func validateIntakeMapping(db *gorm.DB, mapping models.InboxIntakeMapping) error {
	if mapping.Validate() != nil {
		return inboxIntakeProblem{"invalid_mapping"}
	}
	if err := intakeAuthority(db, mapping.OrganizationID, mapping.OwnerID); err != nil {
		return err
	}
	definitions, err := intakeDefinitions(db, mapping)
	if err != nil {
		return err
	}
	for _, input := range mapping.Fields {
		d, exists := definitions[input.DefinitionID]
		if !exists || d.ArchivedAt != nil || d.Validate() != nil {
			return inboxIntakeProblem{"field_unavailable"}
		}
	}
	return nil
}

type inboxIntakeMappingResponse struct {
	models.InboxIntakeMapping
	UnavailableFields []uuid.UUID `json:"unavailable_fields"`
}

func (a *App) ListInboxIntakeMappings(r *fastglue.Request) error {
	org, _, ok := a.intakeRequest(r)
	if !ok {
		return nil
	}
	var rows []models.InboxIntakeMapping
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	if err := db.Where("organization_id=?", org).Order("name,id").Limit(201).Find(&rows).Error; err != nil {
		return a.intakeError(r, err)
	}
	// Explicitly report truncation, rather than presenting partial configuration
	// as a complete set of reusable mappings.
	more := len(rows) > 200
	if more {
		rows = rows[:200]
	}
	ids := []uuid.UUID{}
	for _, row := range rows {
		for _, input := range row.Fields {
			ids = append(ids, input.DefinitionID)
		}
	}
	var fields []models.InboxFieldDefinition
	if len(ids) > 0 {
		if err := db.Where("organization_id=? AND id IN ?", org, ids).Find(&fields).Error; err != nil {
			return a.intakeError(r, err)
		}
	}
	definitions := map[uuid.UUID]models.InboxFieldDefinition{}
	for _, field := range fields {
		definitions[field.ID] = field
	}
	result := make([]inboxIntakeMappingResponse, 0, len(rows))
	for _, row := range rows {
		item := inboxIntakeMappingResponse{InboxIntakeMapping: row, UnavailableFields: []uuid.UUID{}}
		for _, input := range row.Fields {
			d, exists := definitions[input.DefinitionID]
			if !exists || d.ArchivedAt != nil {
				item.UnavailableFields = append(item.UnavailableFields, input.DefinitionID)
			}
		}
		result = append(result, item)
	}
	return r.SendEnvelope(map[string]any{"mappings": result, "has_more": more})
}

type inboxIntakeMappingInput struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	ConsentKey  string                    `json:"consent_key"`
	Fields      []models.InboxIntakeInput `json:"fields"`
	Enabled     bool                      `json:"enabled"`
	Revision    int64                     `json:"revision"`
}

func (a *App) CreateInboxIntakeMapping(r *fastglue.Request) error {
	org, user, ok := a.intakeRequest(r)
	if !ok {
		return nil
	}
	var input inboxIntakeMappingInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	mapping := models.InboxIntakeMapping{OrganizationID: org, OwnerID: user, Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), ConsentKey: input.ConsentKey, Fields: input.Fields, Enabled: false, Revision: 1}
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := lockInboxFieldDefinitions(tx, org); err != nil {
			return err
		}
		if err := validateIntakeMapping(tx, mapping); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.InboxIntakeMapping{}).Where("organization_id=?", org).Count(&count).Error; err != nil {
			return err
		}
		if count >= 200 {
			return inboxIntakeProblem{"mapping_limit"}
		}
		return tx.Create(&mapping).Error
	})
	if err != nil {
		return a.intakeError(r, err)
	}
	a.logAudit(org, user, "inbox_intake_mapping", mapping.ID, models.AuditActionCreated, nil, &mapping)
	return r.SendEnvelope(map[string]any{"mapping": mapping})
}

func (a *App) UpdateInboxIntakeMapping(r *fastglue.Request) error {
	org, user, ok := a.intakeRequest(r)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "mapping_id", "Intake mapping")
	if err != nil {
		return nil
	}
	var input inboxIntakeMappingInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var mapping models.InboxIntakeMapping
	var previous models.InboxIntakeMapping
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := lockInboxFieldDefinitions(tx, org); err != nil {
			return err
		}
		if err := tx.First(&mapping, "organization_id=? AND id=?", org, id).Error; err != nil {
			return err
		}
		if input.Revision != mapping.Revision {
			return inboxIntakeProblem{"revision_changed"}
		}
		previous = mapping
		mapping.OwnerID, mapping.Name, mapping.Description, mapping.ConsentKey, mapping.Fields, mapping.Enabled = user, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), input.ConsentKey, input.Fields, input.Enabled
		mapping.Revision++
		if err := validateIntakeMapping(tx, mapping); err != nil {
			return err
		}
		if mapping.Enabled {
			if _, err := a.integrationEncryptionKey(); err != nil {
				return inboxIntakeProblem{"encryption_unavailable"}
			}
		}
		return tx.Save(&mapping).Error
	})
	if err != nil {
		return a.intakeError(r, err)
	}
	a.logAudit(org, user, "inbox_intake_mapping", mapping.ID, models.AuditActionUpdated, &previous, &mapping)
	return r.SendEnvelope(map[string]any{"mapping": mapping})
}

// Disabling remains available even after a destination is archived or removed.
// It changes no fields or bot settings and invalidates unfinished captures.
func (a *App) DisableInboxIntakeMapping(r *fastglue.Request) error {
	org, user, ok := a.intakeRequest(r)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "mapping_id", "Intake mapping")
	if err != nil {
		return nil
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var mapping, previous models.InboxIntakeMapping
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := lockInboxFieldDefinitions(tx, org); err != nil {
			return err
		}
		if err := tx.First(&mapping, "id=? AND organization_id=?", id, org).Error; err != nil {
			return err
		}
		if mapping.Revision != input.Revision {
			return inboxIntakeProblem{"revision_changed"}
		}
		if err := intakeAuthority(tx, org, user); err != nil {
			return err
		}
		previous = mapping
		mapping.Enabled = false
		mapping.Revision++
		return tx.Save(&mapping).Error
	})
	if err != nil {
		return a.intakeError(r, err)
	}
	a.logAudit(org, user, "inbox_intake_mapping", mapping.ID, models.AuditActionUpdated, &previous, &mapping)
	return r.SendEnvelope(map[string]any{"mapping": mapping})
}
