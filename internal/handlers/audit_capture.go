package handlers

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

const auditRequestContextKey = "internal.audit.request_context"

// AuditRequestContext creates provenance before authentication/authorization.
// Neither request IDs nor origins come from caller-controlled context headers.
func (a *App) AuditRequestContext() fastglue.FastMiddleware {
	return func(r *fastglue.Request) *fastglue.Request {
		a.requestAuditContext(r)
		return r
	}
}

func (a *App) requestAuditContext(r *fastglue.Request) audit.RequestContext {
	if current, ok := r.RequestCtx.UserValue(auditRequestContextKey).(audit.RequestContext); ok {
		return current
	}
	var trusted []netip.Prefix
	if a.Config != nil {
		var err error
		trusted, err = a.Config.Audit.TrustedProxies()
		if err != nil {
			// Config.Load rejects this at startup. A manually constructed App
			// still fails safely to peer-only origin and a fixed diagnostic.
			trusted = nil
			slog.Warn("Security audit proxy policy unavailable", "code", "invalid_proxy_policy")
		}
	}
	peer, _ := netip.AddrFromSlice(r.RequestCtx.RemoteIP())
	value := audit.RequestContext{
		RequestID: uuid.New(),
		Origin:    audit.ResolveOrigin(peer, string(r.RequestCtx.Request.Header.Peek("X-Forwarded-For")), trusted),
	}
	r.RequestCtx.SetUserValue(auditRequestContextKey, value)
	return value
}

// observeSecurity is bounded and best effort for authentication/denial outcomes.
// It does not change the existing HTTP result or spawn untracked work. A storage
// failure is observable through fixed categories, not SQL/request/provider data.
func (a *App) observeSecurity(r *fastglue.Request, event audit.SecurityEvent) {
	provenance := a.requestAuditContext(r)
	provenance.AccountID, provenance.CaseID = event.Context.AccountID, event.Context.CaseID
	event.Context = provenance
	if err := audit.RecordSecurity(a.DB, event); err != nil {
		slog.Warn("Security audit capture failed", "event", event.Kind, "code", audit.ErrorCode(err))
	}
}

// recordRoleSecurity participates in the role mutation's transaction. Unlike
// authentication observations, failure must abort the privileged database write.
func (a *App) recordRoleSecurity(r *fastglue.Request, tx *gorm.DB, orgID, actorID, roleID uuid.UUID, reason audit.Reason) error {
	return audit.RecordSecurity(tx, audit.SecurityEvent{OrganizationID: orgID, ActorID: actorID, ResourceID: roleID, ResourceType: "role", Kind: audit.PermissionChanged, Outcome: audit.Success, Reason: reason, Context: a.requestAuditContext(r)})
}

func (a *App) observeSignIn(r *fastglue.Request, orgID, targetID, actorID uuid.UUID, outcome audit.Outcome, reason audit.Reason) {
	a.observeSecurity(r, audit.SecurityEvent{OrganizationID: orgID, ResourceID: targetID, ActorID: actorID, ResourceType: "user", Kind: audit.SignIn, Outcome: outcome, Reason: reason})
}

func (a *App) observeDenial(r *fastglue.Request, orgID, userID uuid.UUID, resource, action string) {
	// Reject stale session identity instead of attributing denied actions to an
	// account whose session stamp or membership is no longer current.
	if claims, ok := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims); ok {
		if claims.UserID != userID {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := middleware.CurrentIdentity(a.DB.WithContext(ctx), claims); err != nil {
			return
		}
	}
	a.observeSecurity(r, audit.SecurityEvent{OrganizationID: orgID, ResourceID: orgID, ActorID: userID, ResourceType: "permission", Kind: audit.AuthorizationDenied, Outcome: audit.Denied, Reason: audit.PermissionMissing, Permission: resource + ":" + action})
}
