package handlers

import (
	"context"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/shridarpatil/whatomate/internal/middleware"
)

// Emergency case reads consume a durable, separately approved decision for this
// one operation. The case owner still enforces organization, exact triage scope
// and subject conflicts. Neither a role nor an assignment is added here.
func (a *App) authorizeEmergencyCaseRead(ctx context.Context, original addonapi.Actor, caseID, approvalID string) error {
	if a.ExtensionSecurity == nil || caseID == "" || approvalID == "" {
		return addonapi.ErrDenied
	}
	current, err := a.refreshExtensionActor(ctx, original)
	if err != nil || !sameExtensionAccess(original, current) {
		return addonapi.ErrDenied
	}
	claims, _ := ctx.Value(extensionClaimsKey{}).(*middleware.JWTClaims)
	actor, err := a.ExtensionActorFromClaims(ctx, claims)
	if err != nil {
		return addonapi.ErrDenied
	}
	return a.ExtensionSecurity.AuthorizeBreakGlass(ctx, actor, approvalID, caseID, "cases:read")
}
