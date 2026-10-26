package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/extensions/security"
	extensions "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"github.com/zerodha/fastglue"
)

func (a *App) extensionFreshness() time.Duration {
	if a.ExtensionMFAFreshness > 0 {
		return a.ExtensionMFAFreshness
	}
	return 12 * time.Hour
}

// ExtensionActor is the trusted protected-data bridge. API keys never construct
// a protected actor; primary sessions require the exact trusted demo policy.
func (a *App) ExtensionActor(r *fastglue.Request) (extensions.Actor, error) {
	claims, _ := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims)
	return a.ExtensionActorFromClaims(context.WithoutCancel(r.RequestCtx), claims)
}

// ExtensionActorFromClaims accepts only claims already verified by trusted JWT
// authentication. Socket callers must retain the original signed proof and call
// this on every delivery, never cache an Actor or extend its MFA timestamp.
func (a *App) ExtensionActorFromClaims(ctx context.Context, claims *middleware.JWTClaims) (extensions.Actor, error) {
	actor, err := a.extensionPrimaryActor(ctx, claims)
	if err != nil || !actor.HasProtectedAuthentication() {
		return extensions.Actor{}, extensions.ErrDenied
	}
	return actor, nil
}

func (a *App) extensionPrimarySessionDemoAllowed(org uuid.UUID) bool {
	return org != uuid.Nil && a.Config != nil && a.Config.Extensions.ValidatePrimarySessionDemo() == nil && a.Config.Extensions.PrimarySessionDemoOrganizationID == org.String()
}

func extensionAuthenticationPolicy(actor extensions.Actor) string {
	if actor.PrimarySessionAuthorized {
		return "primary-session-demo"
	}
	return "mfa"
}

// Refresh already validates the signed refresh session and subsequently checks
// CurrentIdentity. This changes only the configured demo's factor requirement;
// it does not authorize an actor or manufacture/renew MFA proof in the token.
func (a *App) extensionRefreshRequiresMFA(claims *middleware.JWTClaims) bool {
	return claims != nil && claims.MFAAt != nil && !a.extensionPrimarySessionDemoAllowed(claims.OrganizationID)
}

func (a *App) extensionPrimaryActor(ctx context.Context, claims *middleware.JWTClaims) (extensions.Actor, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if a.ExtensionSecurity == nil || claims == nil || (claims.Subject != "access" && claims.Subject != "ws") || claims.UserID == uuid.Nil || claims.OrganizationID == uuid.Nil {
		return extensions.Actor{}, extensions.ErrDenied
	}
	if a.Config != nil && a.Config.Extensions.ValidatePrimarySessionDemo() != nil {
		return extensions.Actor{}, extensions.ErrDenied
	}
	if err := middleware.ValidateSession(ctx, a.Redis, claims); err != nil {
		return extensions.Actor{}, err
	}
	user, err := middleware.CurrentIdentity(a.DB.WithContext(ctx), claims)
	if err != nil {
		return extensions.Actor{}, err
	}
	actor := extensions.Actor{OrganizationID: claims.OrganizationID.String(), UserID: user.ID.String(), SessionID: claims.SessionID}
	demo := a.extensionPrimarySessionDemoAllowed(claims.OrganizationID)
	if demo && user.OrganizationID != claims.OrganizationID {
		// CurrentIdentity's ordinary host superadmin organization shortcut is not
		// authority for protected data under the demo policy.
		var membership models.UserOrganization
		if err := a.DB.WithContext(ctx).Where("user_id = ? AND organization_id = ?", user.ID, claims.OrganizationID).First(&membership).Error; err != nil {
			return extensions.Actor{}, extensions.ErrDenied
		}
		user.RoleID = membership.RoleID
	}
	// Read durable explicit grants. HasPermission has administrator shortcuts and
	// cached grants, neither of which is appropriate for protected extensions.
	if user.RoleID != nil {
		var role models.CustomRole
		if err := a.DB.WithContext(ctx).Preload("Permissions").Where("id = ? AND organization_id = ?", *user.RoleID, claims.OrganizationID).First(&role).Error; err != nil {
			return extensions.Actor{}, err
		}
		for _, p := range role.Permissions {
			actor.Permissions = append(actor.Permissions, p.Resource+":"+p.Action)
		}
	}
	// This policy bit is assigned only after durable session, identity,
	// membership and explicit role permissions have all been rechecked.
	actor.PrimarySessionAuthorized = demo
	if claims.MFAFresh(time.Now(), a.extensionFreshness()) {
		status, err := a.ExtensionSecurity.Generation(ctx, actor.OrganizationID, actor.UserID)
		if err != nil && !actor.PrimarySessionAuthorized {
			return extensions.Actor{}, err
		}
		actor.MFA = err == nil && status.Enabled() && status.Generation() == claims.MFAGeneration
		if actor.MFA {
			actor.MFAGeneration = claims.MFAGeneration
		}
	}
	return actor, nil
}
func (a *App) extensionPrimary(r *fastglue.Request) (extensions.Actor, *middleware.JWTClaims, error) {
	claims, _ := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims)
	actor, err := a.extensionPrimaryActor(context.WithoutCancel(r.RequestCtx), claims)
	return actor, claims, err
}
func extensionSecurityError(r *fastglue.Request, err error) error {
	if errors.Is(err, security.ErrDenied) || errors.Is(err, extensions.ErrDenied) || errors.Is(err, middleware.ErrSessionRevoked) {
		return r.SendErrorEnvelope(403, "MFA or explicit security permission required", nil, "extension_access_denied")
	}
	return r.SendErrorEnvelope(503, "Security operation unavailable", nil, "extension_security_unavailable")
}
func (a *App) ExtensionMFAStatus(r *fastglue.Request) error {
	actor, _, err := a.extensionPrimary(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	status, err := a.ExtensionSecurity.Generation(context.WithoutCancel(r.RequestCtx), actor.OrganizationID, actor.UserID)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	return r.SendEnvelope(map[string]any{"enabled": status.Enabled(), "generation": status.Generation(), "verified": actor.MFA, "required": !actor.PrimarySessionAuthorized, "authentication_policy": extensionAuthenticationPolicy(actor)})
}
func (a *App) ExtensionMFABegin(r *fastglue.Request) error {
	actor, _, err := a.extensionPrimary(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	enrollment, err := a.ExtensionSecurity.BeginEnrollment(context.WithoutCancel(r.RequestCtx), actor)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	return r.SendEnvelope(map[string]any{"id": enrollment.ID(), "secret": enrollment.Secret().Reveal(), "expires_at": enrollment.ExpiresAt()})
}
func (a *App) ExtensionMFAConfirm(r *fastglue.Request) error {
	actor, claims, err := a.extensionPrimary(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	var body struct {
		EnrollmentID string `json:"enrollment_id"`
		Code         string `json:"code"`
	}
	if a.decodeRequest(r, &body) != nil {
		return nil
	}
	activation, err := a.ExtensionSecurity.ConfirmEnrollment(context.WithoutCancel(r.RequestCtx), actor, body.EnrollmentID, body.Code)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	if err = a.extensionIssueMFA(r, claims, activation.Verification()); err != nil {
		return extensionSecurityError(r, err)
	}
	codes := []string{}
	for _, c := range activation.RecoveryCodes() {
		codes = append(codes, c.Reveal())
	}
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	return r.SendEnvelope(map[string]any{"recovery_codes": codes, "generation": activation.Verification().Generation()})
}
func (a *App) ExtensionMFAChallenge(r *fastglue.Request) error {
	actor, claims, err := a.extensionPrimary(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	var body struct {
		Code     string `json:"code"`
		Recovery bool   `json:"recovery"`
	}
	if a.decodeRequest(r, &body) != nil {
		return nil
	}
	var proof security.Verification
	if body.Recovery {
		proof, err = a.ExtensionSecurity.VerifyRecovery(context.WithoutCancel(r.RequestCtx), actor.OrganizationID, actor.UserID, body.Code)
	} else {
		proof, err = a.ExtensionSecurity.VerifyTOTP(context.WithoutCancel(r.RequestCtx), actor.OrganizationID, actor.UserID, body.Code)
	}
	if err != nil {
		return extensionSecurityError(r, err)
	}
	if err = a.extensionIssueMFA(r, claims, proof); err != nil {
		return extensionSecurityError(r, err)
	}
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	return r.SendEnvelope(map[string]any{"verified": true, "generation": proof.Generation()})
}
func (a *App) ExtensionMFARevoke(r *fastglue.Request) error {
	actor, err := a.ExtensionActor(r)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	if !actor.MFA {
		return extensionSecurityError(r, extensions.ErrDenied)
	}
	var body struct {
		Generation uint64 `json:"generation"`
	}
	if a.decodeRequest(r, &body) != nil {
		return nil
	}
	status, err := a.ExtensionSecurity.Revoke(context.WithoutCancel(r.RequestCtx), actor, body.Generation)
	if err != nil {
		return extensionSecurityError(r, err)
	}
	return r.SendEnvelope(map[string]any{"enabled": status.Enabled(), "generation": status.Generation()})
}

func (a *App) extensionIssueMFA(r *fastglue.Request, primary *middleware.JWTClaims, proof security.Verification) error {
	if primary == nil || proof.UserID() != primary.UserID.String() || proof.OrganizationID() != primary.OrganizationID.String() {
		return extensions.ErrDenied
	}
	user, err := middleware.CurrentIdentity(a.DB.WithContext(context.WithoutCancel(r.RequestCtx)), primary)
	if err != nil {
		return err
	}
	user.OrganizationID = primary.OrganizationID
	access, err := a.generateAccessToken(user, primary.SessionVersion, primary.SessionFamily)
	if err != nil {
		return err
	}
	claims, err := middleware.ParseToken(a.Config.JWT.Secret, access, "access")
	if err != nil {
		return err
	}
	claims.MFAAt = jwt.NewNumericDate(proof.VerifiedAt())
	claims.MFAGeneration = proof.Generation()
	access, err = a.signMFAClaims(claims)
	if err != nil {
		return err
	}
	if _, err = a.ExtensionActorFromClaims(context.WithoutCancel(r.RequestCtx), claims); err != nil {
		return err
	}
	refresh, err := a.generateRefreshToken(user, access)
	if err != nil {
		return err
	}
	a.setAuthCookies(r, access, refresh)
	return nil
}
func (a *App) signMFAClaims(claims *middleware.JWTClaims) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(a.Config.JWT.Secret))
}

// preserveMFA copies only signed proof from the authenticated predecessor. A
// refreshed token cannot turn primary authentication into MFA or reset its age.
func (a *App) preserveMFA(encoded string, source *middleware.JWTClaims, purpose string) (string, error) {
	claims, err := middleware.ParseToken(a.Config.JWT.Secret, encoded, purpose)
	if err != nil {
		return "", err
	}
	if source != nil {
		claims.MFAAt = source.MFAAt
		claims.MFAGeneration = source.MFAGeneration
	}
	return a.signMFAClaims(claims)
}

func (a *App) extensionValidateMFA(ctx context.Context, claims *middleware.JWTClaims) error {
	if a.ExtensionSecurity == nil || !claims.MFAFresh(time.Now(), a.extensionFreshness()) {
		return extensions.ErrDenied
	}
	current, err := a.ExtensionSecurity.Generation(ctx, claims.OrganizationID.String(), claims.UserID.String())
	if err != nil {
		return err
	}
	if !current.Enabled() || current.Generation() != claims.MFAGeneration {
		return extensions.ErrDenied
	}
	return nil
}
