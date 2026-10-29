package handlers

import (
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/zerodha/fastglue"
)

func (a *App) sendPlanError(r *fastglue.Request, err error) (bool, error) {
	resource, ok := plan.Classify(err)
	if !ok {
		return false, nil
	}
	return true, r.SendErrorEnvelope(409, "Instance resource allowance reached", map[string]any{"code": "plan_limit_reached", "resource": resource}, "")
}
