package handlers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// sendPlanError maps the same guard errors for ordinary, imported, and restored records.
func (a *App) sendPlanError(r *fastglue.Request, err error) (bool, error) {
	if plan.IsSupportProtected(err) {
		return true, r.SendErrorEnvelope(fasthttp.StatusForbidden, "Support account is protected", nil, "support_protected")
	}
	if resource, ok := plan.Classify(err); ok {
		message := "Plan limit reached"
		switch resource {
		case "users":
			message = "User limit reached"
		case "numbers":
			message = "WhatsApp number limit reached"
		case "storage":
			message = "Storage limit reached"
		case "organizations":
			message = "Each managed instance supports one organization"
		}
		return true, r.SendErrorEnvelope(fasthttp.StatusConflict, message, map[string]string{"resource": resource}, "plan_limit_reached")
	}
	return false, nil
}

func (a *App) isSupportEmail(email string) bool {
	return a.Config != nil && a.Config.Plan.Enabled && strings.EqualFold(strings.TrimSpace(email), a.Config.Plan.SupportEmail)
}

func (a *App) protectSupportUser(r *fastglue.Request, id uuid.UUID) (bool, error) {
	if a.Config == nil || !a.Config.Plan.Enabled {
		return false, nil
	}
	var email string
	if err := a.DB.Table("users").Select("email").Where("id = ?", id).Scan(&email).Error; err != nil {
		return true, r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to verify support protection", nil, "")
	}
	if a.isSupportEmail(email) {
		return true, r.SendErrorEnvelope(fasthttp.StatusForbidden, "Support account is protected", nil, "support_protected")
	}
	return false, nil
}
