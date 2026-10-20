package handlers

import (
	"context"
	"github.com/shridarpatil/whatomate/pkg/extensions/security"
	"github.com/zerodha/fastglue"
	"time"
)

func (a *App) ExtensionEmergencyRequest(r *fastglue.Request) error {
	actor, err := a.ExtensionActor(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	var body struct {
		Resource        string `json:"resource"`
		Permission      string `json:"permission"`
		Reason          string `json:"reason"`
		DurationSeconds int64  `json:"duration_seconds"`
	}
	if a.decodeRequest(r, &body) != nil {
		return nil
	}
	// Bound conversion before multiplying a caller-supplied integer duration.
	if body.DurationSeconds <= 0 || body.DurationSeconds > 86400 {
		return extensionSecurityError(r, security.ErrDenied)
	}
	grant, err := a.ExtensionSecurity.RequestBreakGlass(context.WithoutCancel(r.RequestCtx), actor, body.Resource, body.Permission, body.Reason, time.Duration(body.DurationSeconds)*time.Second)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	return r.SendEnvelope(grant)
}
func (a *App) ExtensionEmergencyApprove(r *fastglue.Request) error {
	actor, err := a.ExtensionActor(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	var body struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}
	if a.decodeRequest(r, &body) != nil {
		return nil
	}
	grant, err := a.ExtensionSecurity.ApproveBreakGlass(context.WithoutCancel(r.RequestCtx), actor, body.ID, body.Version)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	return r.SendEnvelope(grant)
}
func (a *App) ExtensionEmergencyUse(r *fastglue.Request) error {
	actor, err := a.ExtensionActor(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	var body struct {
		ID         string `json:"id"`
		Resource   string `json:"resource"`
		Permission string `json:"permission"`
	}
	if a.decodeRequest(r, &body) != nil {
		return nil
	}
	if err := a.ExtensionSecurity.AuthorizeBreakGlass(context.WithoutCancel(r.RequestCtx), actor, body.ID, body.Resource, body.Permission); err != nil {
		return extensionSecurityError(r, err)
	}
	// This is a decision for this exact check, never a reusable access token or a
	// role update. Each protected operation must perform its own authorization.
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	return r.SendEnvelope(map[string]any{"authorized": true, "resource": body.Resource, "permission": body.Permission})
}
func (a *App) ExtensionEmergencyRevoke(r *fastglue.Request) error {
	actor, err := a.ExtensionActor(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	var body struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}
	if a.decodeRequest(r, &body) != nil {
		return nil
	}
	grant, err := a.ExtensionSecurity.RevokeBreakGlass(context.WithoutCancel(r.RequestCtx), actor, body.ID, body.Version)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	return r.SendEnvelope(grant)
}
