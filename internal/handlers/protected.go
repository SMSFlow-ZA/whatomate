package handlers

import (
	"context"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// GenericOrganizationAllowed is deliberately conservative until every derived
// generic host surface supports resource policies. No role, same-org or admin
// shortcut applies. Include deleted records to protect queued/cached payloads.
func (a *App) GenericOrganizationAllowed(ctx context.Context, orgID uuid.UUID) bool {
	if a.DB == nil || orgID == uuid.Nil {
		return false
	}
	if _, fast := ctx.(*fasthttp.RequestCtx); fast {
		ctx = context.WithoutCancel(ctx)
	}
	return models.GenericOrganizationAllowed(ctx, a.DB, orgID)
}

// DenyGenericProtectedOrganization is used after authentication by generic API
// route middleware. Extension/auth bootstrap routes must be registered outside
// this gate and must implement their own explicit current-actor authorization.
func (a *App) DenyGenericProtectedOrganization(r *fastglue.Request) (bool, error) {
	orgID, _, err := a.getOrgAndUserID(r)
	if err == nil && a.GenericOrganizationAllowed(r.RequestCtx, orgID) {
		return false, nil
	}
	return true, r.SendErrorEnvelope(fasthttp.StatusForbidden, "Protected resource access denied", nil, "protected_access_denied")
}

// AuthorizeProtection checks a persisted tuple against a current authenticated
// actor, never a request-body Actor. Module APIs must call it on each read/write.
func (a *App) AuthorizeProtection(r *fastglue.Request, orgID uuid.UUID, fields models.ProtectionFields, action string) error {
	protection, err := fields.Value(orgID)
	if err != nil || a.ExtensionGuard == nil {
		return ext.ErrDenied
	}
	actor, err := a.ExtensionActor(r)
	if err != nil {
		return ext.ErrDenied
	}
	return a.ExtensionGuard.Authorize(r.RequestCtx, actor, protection, action)
}
