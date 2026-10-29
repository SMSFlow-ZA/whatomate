package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/zerodha/fastglue"
)

// GetPlan exposes instance aggregate usage only to authenticated superadmins.
func (a *App) GetPlan(r *fastglue.Request) error {
	_, user, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	if !a.IsSuperAdmin(user) {
		return r.SendErrorEnvelope(403, "Instance usage requires a super administrator", nil, "")
	}
	if !a.Config.Plan.Enabled {
		return r.SendEnvelope(map[string]any{"enabled": false, "enforced": false})
	}
	cfg, usage, err := plan.Snapshot(a.DB)
	if err != nil {
		return r.SendErrorEnvelope(503, "Instance usage temporarily unavailable", nil, "")
	}
	return r.SendEnvelope(map[string]any{"enabled": true, "enforced": true, "limits": map[string]any{"max_users": cfg.MaxUsers, "max_numbers": cfg.MaxNumbers, "max_organizations": cfg.MaxOrganizations}, "usage": map[string]any{"users": usage.Users, "numbers": usage.Numbers, "organizations": usage.Organizations}})
}
