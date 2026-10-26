package handlers

import (
	"context"
	"encoding/json"
	"sort"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
)

type extensionClaimsKey struct{}

// Current membership is deliberately separate from session authentication: a
// publication reviewer need not be logged in when a publisher submits a version.
func (a *App) extensionMember(ctx context.Context, organizationID, userID string) (addonapi.Actor, error) {
	org, e := uuid.Parse(organizationID)
	if e != nil {
		return addonapi.Actor{}, addonapi.ErrDenied
	}
	id, e := uuid.Parse(userID)
	if e != nil || a.DB == nil || a.ExtensionAccess == nil {
		return addonapi.Actor{}, addonapi.ErrDenied
	}
	var user models.User
	if e = a.DB.WithContext(ctx).Where("id = ? AND organization_id = ? AND is_active = ?", id, org, true).First(&user).Error; e != nil {
		return addonapi.Actor{}, addonapi.ErrDenied
	}
	actor := addonapi.Actor{OrganizationID: org.String(), UserID: id.String()}
	if user.RoleID != nil {
		var role models.CustomRole
		if e = a.DB.WithContext(ctx).Preload("Permissions").Where("id = ? AND organization_id = ?", *user.RoleID, org).First(&role).Error; e != nil {
			return addonapi.Actor{}, addonapi.ErrDenied
		}
		for _, p := range role.Permissions {
			actor.Permissions = append(actor.Permissions, p.Resource+":"+p.Action)
		}
	}
	return a.ExtensionAccess.Load(ctx, actor)
}

// Claims are inserted by the trusted HTTP bridge. Request JSON cannot construct
// this value. Long operations revalidate the original session and MFA proof.
func (a *App) refreshExtensionActor(ctx context.Context, original addonapi.Actor) (addonapi.Actor, error) {
	claims, _ := ctx.Value(extensionClaimsKey{}).(*middleware.JWTClaims)
	current, e := a.ExtensionActorFromClaims(ctx, claims)
	if e != nil || current.OrganizationID != original.OrganizationID || current.UserID != original.UserID || current.SessionID != original.SessionID {
		return addonapi.Actor{}, addonapi.ErrDenied
	}
	return a.ExtensionAccess.Load(ctx, addonapi.Actor{OrganizationID: current.OrganizationID, UserID: current.UserID, SessionID: current.SessionID, MFA: current.MFA, PrimarySessionAuthorized: current.PrimarySessionAuthorized, Permissions: current.Permissions})
}

func (a *App) authorizeExtensionKnowledge(ctx context.Context, org, user, permission, audience string) bool {
	actor, e := a.extensionMember(ctx, org, user)
	return e == nil && actor.AudienceAllowed(permission, audience)
}

func sameExtensionAccess(before, after addonapi.Actor) bool {
	normalize := func(actor addonapi.Actor) string {
		actor.Permissions = append([]string{}, actor.Permissions...)
		sort.Strings(actor.Permissions)
		actor.Aliases = append([]string{}, actor.Aliases...)
		sort.Strings(actor.Aliases)
		actor.Grants = append([]addonapi.Grant{}, actor.Grants...)
		sort.Slice(actor.Grants, func(i, j int) bool {
			x, _ := json.Marshal(actor.Grants[i])
			y, _ := json.Marshal(actor.Grants[j])
			return string(x) < string(y)
		})
		b, _ := json.Marshal(actor)
		return string(b)
	}
	return normalize(before) == normalize(after)
}
