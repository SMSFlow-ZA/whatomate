package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/zerodha/fastglue"
)

func (a *App) requireInboxAuth(r *fastglue.Request, resource, action string) (uuid.UUID, uuid.UUID, error) {
	viewer, ok := a.freshInboxAuthority(r, resource, action)
	if !ok {
		return uuid.Nil, uuid.Nil, errEnvelopeSent
	}
	return viewer.Org, viewer.User, nil
}

// HTTP inbox access must not depend on receiving a live permission notification.
// Load current database authority and reconcile only this viewer's caches before
// older scoped helpers use them. A cache/storage outage fails closed.
func (a *App) freshInboxAuthority(r *fastglue.Request, resource, action string) (auditViewer, bool) {
	org, user, claims, err := auditReadIdentity(r, a)
	viewer := auditViewer{}
	if err == nil {
		db, cancel := boundedIntakeDB(a.DB)
		defer cancel()
		if requested := string(r.RequestCtx.Request.Header.Peek("X-Organization-ID")); requested != "" {
			selected, parseErr := uuid.Parse(requested)
			if parseErr != nil || selected != org {
				err = errAuditForbidden // Never silently fall back to the home organisation.
			}
		}
		if err == nil && claims != nil {
			var authenticatedOrg uuid.UUID
			switch value := r.RequestCtx.UserValue(middleware.ContextKeyOrganizationID).(type) {
			case uuid.UUID:
				authenticatedOrg = value
			case string:
				authenticatedOrg, _ = uuid.Parse(value)
			}
			if authenticatedOrg != claims.OrganizationID || user != claims.UserID {
				err = errAuditUnauthenticated
			} else {
				// Preserve the authenticated identity/stamp/session. Only a local
				// copy adopts the already-resolved selected organisation; current
				// membership/super status is checked again by the database loader.
				selected := *claims
				selected.OrganizationID = org
				claims = &selected
			}
		}
		if err == nil {
			viewer, err = loadAuditViewer(db, org, user, claims)
		}
		if err == nil && !viewer.has(resource, action) {
			err = errAuditForbidden
		}
		if err == nil {
			err = a.reconcileInboxLivePermissionCache(db.Statement.Context, viewer)
		}
	}
	if err == nil {
		return viewer, true
	}
	switch {
	case errors.Is(err, errAuditUnauthenticated):
		_ = r.SendErrorEnvelope(401, "Sign in again to continue", nil, "inbox_identity_unavailable")
	case errors.Is(err, errAuditForbidden):
		a.observeDenial(r, org, user, resource, action)
		_ = r.SendErrorEnvelope(403, "You no longer have permission for this inbox action", nil, "inbox_access_denied")
	default:
		_ = r.SendErrorEnvelope(503, "Inbox access could not be verified. Try again.", nil, "inbox_authority_unavailable")
	}
	return auditViewer{}, false
}
