package handlers

import (
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// GetPlan exposes aggregate usage only. Configuration and bootstrap credentials
// cannot be changed or obtained through this endpoint.
func (a *App) GetPlan(r *fastglue.Request) error {
	if _, _, err := a.getOrgAndUserID(r); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	cfg := a.Config.Plan
	if !cfg.Enabled {
		return r.SendEnvelope(map[string]any{"enabled": false, "enforced": false})
	}
	cfg, users, numbers, storage, err := plan.UsageSnapshot(a.DB, cfg, []string{a.getMediaStoragePath(), a.getAudioDir()})
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Plan usage is temporarily unavailable", nil, "")
	}
	return r.SendEnvelope(map[string]any{
		"enabled": true, "enforced": true, "support_protected": true,
		"limits":     map[string]any{"max_users": cfg.MaxUsers, "max_numbers": cfg.MaxNumbers, "max_storage_bytes": cfg.MaxStorageBytes},
		"usage":      map[string]any{"users": users, "numbers": numbers, "storage_bytes": storage},
		"over_limit": map[string]bool{"users": users > int64(cfg.MaxUsers), "numbers": numbers > int64(cfg.MaxNumbers), "storage": storage > cfg.MaxStorageBytes},
		"source_url": cfg.SourceURL,
	})
}

// CorrespondingSource is available even before sign-in.
func (a *App) CorrespondingSource(r *fastglue.Request) error {
	source := a.BuildSourceURL
	if a.Config.Plan.Enabled {
		source = a.Config.Plan.SourceURL
	}
	if source == "" {
		source = "https://github.com/SMSFlow-ZA/whatomate/tree/codex/plan-limits"
	}
	r.RequestCtx.Redirect(source, fasthttp.StatusFound)
	return nil
}
