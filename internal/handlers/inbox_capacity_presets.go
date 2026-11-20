package handlers

import (
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *App) ListInboxCapacityPresets(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceTeams, models.ActionRead)
	if err != nil {
		return nil
	}
	presets := []models.InboxCapacityPreset{}
	if err := a.DB.Where("organization_id=?", org).Order("name,id").Find(&presets).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"presets": presets})
}
func (a *App) SaveInboxCapacityPreset(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceTeams, models.ActionWrite)
	if err != nil {
		return nil
	}
	var input struct {
		Name      string `json:"name"`
		MaxActive *int   `json:"max_active"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 100 || input.MaxActive == nil || *input.MaxActive < 0 || *input.MaxActive > 10000 {
		return a.inboxError(r, errInboxTransition)
	}
	preset := models.InboxCapacityPreset{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, Name: input.Name, MaxActive: *input.MaxActive}
	if raw, ok := r.RequestCtx.UserValue("id").(string); ok && raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return a.inboxError(r, errInboxTransition)
		}
		if err := a.DB.First(&preset, "id=? AND organization_id=?", id, org).Error; err != nil {
			return a.inboxError(r, err)
		}
		preset.Name, preset.MaxActive = input.Name, *input.MaxActive
	}
	if err := a.DB.Save(&preset).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"preset": preset})
}
func (a *App) DeleteInboxCapacityPreset(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceTeams, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "capacity preset")
	if err != nil {
		return nil
	}
	result := a.DB.Where("id=? AND organization_id=?", id, org).Delete(&models.InboxCapacityPreset{})
	if result.Error != nil {
		return a.inboxError(r, result.Error)
	}
	if result.RowsAffected == 0 {
		return a.inboxError(r, gorm.ErrRecordNotFound)
	}
	return r.SendEnvelope(map[string]bool{"deleted": true})
}

type inboxPresetApplication struct {
	TeamID       uuid.UUID   `json:"team_id"`
	AccountID    uuid.UUID   `json:"account_id"`
	AgentIDs     []uuid.UUID `json:"agent_ids"`
	Confirmation string      `json:"confirmation"`
}
type inboxPresetChange struct {
	AgentID uuid.UUID `json:"agent_id"`
	Name    string    `json:"name"`
	Before  int       `json:"before"`
	After   int       `json:"after"`
}

func (a *App) applyInboxPreset(r *fastglue.Request, apply bool) error {
	org, user, err := a.requireAuth(r, models.ResourceTeams, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "capacity preset")
	if err != nil {
		return nil
	}
	var input inboxPresetApplication
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if len(input.AgentIDs) == 0 || len(input.AgentIDs) > 100 {
		return a.inboxError(r, errInboxTransition)
	}
	var changes []inboxPresetChange
	confirmation := ""
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockInboxCapacity(tx, org, input.AccountID); err != nil {
			return err
		}
		var preset models.InboxCapacityPreset
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&preset, "id=? AND organization_id=?", id, org).Error; err != nil {
			return err
		}
		var account models.WhatsAppAccount
		if err := tx.First(&account, "id=? AND organization_id=?", input.AccountID, org).Error; err != nil {
			return err
		}
		var team models.Team
		if err := tx.First(&team, "id=? AND organization_id=? AND is_active", input.TeamID, org).Error; err != nil {
			return err
		}
		if err := tx.Table("team_members tm").Joins("JOIN users u ON u.id=tm.user_id AND u.deleted_at IS NULL").Joins("JOIN user_organizations uo ON uo.deleted_at IS NULL AND uo.user_id=u.id AND uo.organization_id=?", org).Joins("LEFT JOIN inbox_agent_capacities cap ON cap.agent_id=u.id AND cap.account_id=? AND cap.organization_id=?", input.AccountID, org).Where("tm.team_id=? AND tm.user_id IN ? AND tm.deleted_at IS NULL", input.TeamID, input.AgentIDs).Select("u.id AS agent_id,u.full_name AS name,COALESCE(cap.max_active,0) AS before,?::integer AS after", preset.MaxActive).Order("u.id").Scan(&changes).Error; err != nil {
			return err
		}
		if len(changes) != len(input.AgentIDs) {
			return errInboxTransition
		}
		confirmation = workflowDigest([]any{org, user, preset.ID, preset.UpdatedAt, input.TeamID, input.AccountID, changes})
		if !apply {
			return nil
		}
		if confirmation != input.Confirmation {
			return errInboxConflict
		}
		for _, change := range changes {
			limit := models.InboxAgentCapacity{OrganizationID: org, AccountID: input.AccountID, AgentID: change.AgentID, MaxActive: change.After}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&limit).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"changes": changes, "confirmation": confirmation, "applied": apply})
}
func (a *App) PreviewInboxCapacityPreset(r *fastglue.Request) error {
	return a.applyInboxPreset(r, false)
}
func (a *App) ApplyInboxCapacityPreset(r *fastglue.Request) error { return a.applyInboxPreset(r, true) }
