package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

func (a *App) visibleInboxWorkflows(db *gorm.DB, org, user uuid.UUID) *gorm.DB {
	return db.Where("organization_id=?", org).Where("kind='rule' OR COALESCE(visibility,'organization') IN ('','organization') OR (visibility='personal' AND created_by_id=?) OR (visibility='team' AND team_id IN (SELECT tm.team_id FROM team_members tm JOIN teams t ON t.id=tm.team_id AND t.organization_id=? AND t.is_active AND t.deleted_at IS NULL WHERE tm.user_id=? AND tm.deleted_at IS NULL))", user, org, user)
}

func (a *App) validInboxWorkflowTeam(db *gorm.DB, org uuid.UUID, w models.InboxWorkflow) bool {
	if w.Kind == "rule" {
		return (w.Visibility == "" || w.Visibility == "organization") && w.TeamID == nil
	}
	if w.Visibility != "team" {
		return w.TeamID == nil
	}
	if w.TeamID == nil {
		return false
	}
	var count int64
	return db.Model(&models.Team{}).Where("id=? AND organization_id=? AND is_active", *w.TeamID, org).Count(&count).Error == nil && count == 1
}
