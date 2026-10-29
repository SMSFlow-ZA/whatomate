package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/zerodha/fastglue"
)

// GetPlan returns instance aggregate usage only to an authenticated superadmin.
func (a *App) GetPlan(r *fastglue.Request) error {
	_, user, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	if !a.IsSuperAdmin(user) {
		return r.SendErrorEnvelope(403, "Instance usage requires a super administrator", nil, "")
	}
	cfg := a.Config.Plan
	if !cfg.Enabled {
		return r.SendEnvelope(map[string]any{"enabled": false, "enforced": false})
	}
	cfg, users, numbers, storage, err := plan.UsageSnapshot(a.DB, cfg, []string{a.getMediaStoragePath(), a.getAudioDir()})
	if err != nil {
		return r.SendErrorEnvelope(503, "Instance usage temporarily unavailable", nil, "")
	}
	var organizations int64
	if err := a.DB.Table("organizations").Where("deleted_at IS NULL").Count(&organizations).Error; err != nil {
		return r.SendErrorEnvelope(503, "Instance usage temporarily unavailable", nil, "")
	}
	return r.SendEnvelope(map[string]any{"enabled": true, "enforced": true, "limits": map[string]any{"max_users": cfg.MaxUsers, "max_numbers": cfg.MaxNumbers, "max_storage_bytes": cfg.MaxStorageBytes, "max_organizations": cfg.MaxOrganizations}, "usage": map[string]any{"users": users, "numbers": numbers, "storage_bytes": storage, "organizations": organizations}})
}
