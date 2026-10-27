package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// DuplicateChatbotFlow copies configuration only. The new flow always starts
// disabled, so copying triggers cannot activate another conversation handler.
func (a *App) DuplicateChatbotFlow(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(401, "Unauthorized", nil, "")
	}
	if !a.HasPermission(userID, models.ResourceFlowsChatbot, models.ActionRead, orgID) ||
		!a.HasPermission(userID, models.ResourceFlowsChatbot, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(403, "Permission denied", nil, "")
	}
	id, err := parsePathUUID(r, "id", "flow")
	if err != nil {
		return nil
	}
	var source models.ChatbotFlow
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.SendErrorEnvelope(404, "Flow not found", nil, "")
		}
		a.Log.Error("Failed to load flow for duplication", "error", err)
		return r.SendErrorEnvelope(500, "Failed to duplicate flow", nil, "")
	}

	copy := source
	copy.BaseModel = models.BaseModel{ID: uuid.New()}
	// Names are PostgreSQL varchar(255), whose bound counts characters, not bytes.
	name := []rune(source.Name)
	const suffix = " (Copy)"
	if len(name) > 255-len(suffix) {
		name = name[:255-len(suffix)]
	}
	copy.Name = string(name) + suffix
	copy.IsEnabled = false
	copy.CreatedByID, copy.UpdatedByID = &userID, &userID
	copy.Organization, copy.InitialTemplate, copy.CreatedBy, copy.UpdatedBy = nil, nil, nil, nil
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		// GORM substitutes default:true for a zero bool on Create. Omit that
		// field then explicitly disable it inside the same transaction: no
		// enabled copy is ever visible to workers or another request.
		if err := tx.Omit("IsEnabled").Create(&copy).Error; err != nil {
			return err
		}
		return tx.Model(&copy).Update("is_enabled", false).Error
	}); err != nil {
		a.Log.Error("Failed to duplicate flow", "error", err)
		return r.SendErrorEnvelope(500, "Failed to duplicate flow", nil, "")
	}
	copy.IsEnabled = false
	a.InvalidateChatbotFlowsCache(orgID)
	// Keep credentials embedded in graph HTTP nodes out of the audit payload.
	a.logAudit(orgID, userID, "chatbot_flow", copy.ID, models.AuditActionCreated, nil,
		map[string]any{"source_flow_id": source.ID, "name": copy.Name, "is_enabled": false})
	return r.SendEnvelope(map[string]any{"id": copy.ID, "message": "Flow duplicated successfully", "enabled": false})
}
