package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"strings"
	"time"
	"unicode"
)

// BreakGlass is a detached read-only approval snapshot. Authorization always
// reads current durable approval, expiry, revocation and credential generation.
type BreakGlass struct {
	id, organizationID, userID, resource, permission, reason, approvedBy string
	generation, version                                                  uint64
	requestedAt, expiresAt                                               time.Time
	approvedAt, revokedAt                                                sql.NullTime
}

func (g BreakGlass) ID() string             { return g.id }
func (g BreakGlass) OrganizationID() string { return g.organizationID }
func (g BreakGlass) UserID() string         { return g.userID }
func (g BreakGlass) ResourceID() string     { return g.resource }
func (g BreakGlass) Permission() string     { return g.permission }
func (g BreakGlass) Reason() string         { return g.reason }
func (g BreakGlass) ApprovedBy() string     { return g.approvedBy }
func (g BreakGlass) ExpiresAt() time.Time   { return g.expiresAt }
func (g BreakGlass) Version() uint64        { return g.version }
func (g BreakGlass) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID, OrganizationID, UserID, ResourceID, Permission, Reason, ApprovedBy string
		CredentialGeneration, Version                                          uint64
		RequestedAt, ExpiresAt                                                 time.Time
		Approved, Revoked                                                      bool
	}{g.id, g.organizationID, g.userID, g.resource, g.permission, g.reason, g.approvedBy, g.generation, g.version, g.requestedAt, g.expiresAt, g.approvedAt.Valid, g.revokedAt.Valid})
}
func validReason(reason string) bool {
	if len(reason) < 3 || len(reason) > 512 || strings.TrimSpace(reason) == "" {
		return false
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func loadBreakGlass(ctx context.Context, tx *sql.Tx, org, id string) (BreakGlass, error) {
	var g BreakGlass
	e := tx.QueryRowContext(ctx, `SELECT id,organization_id,user_id,resource_id,permission,reason,credential_generation,requested_at,expires_at,approved_by,approved_at,revoked_at,version FROM extension_security_breakglass WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&g.id, &g.organizationID, &g.userID, &g.resource, &g.permission, &g.reason, &g.generation, &g.requestedAt, &g.expiresAt, &g.approvedBy, &g.approvedAt, &g.revokedAt, &g.version)
	return g, e
}
func (s *Service) denyBreakGlass(ctx context.Context, a ext.Actor, action, resource string) error {
	if !identity(a.OrganizationID, a.UserID) {
		return ErrDenied
	}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, action+".denied", resource, "permission_or_mfa_required")
	})
	if e != nil {
		return e
	}
	return ErrDenied
}
func (s *Service) RequestBreakGlass(ctx context.Context, a ext.Actor, resource, permission, reason string, duration time.Duration) (BreakGlass, error) {
	if !actorAllowed(a, BreakGlassRequestPermission) || !a.MFA || a.SessionID == "" || resource == "" || len(resource) > 256 || !s.allowed[permission] || !validReason(reason) || duration <= 0 || duration > s.maxBreakGlass {
		return BreakGlass{}, s.denyBreakGlass(ctx, a, "breakglass.request", resource)
	}
	token, e := randomToken()
	if e != nil {
		return BreakGlass{}, e
	}
	var result BreakGlass
	var denied bool
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := loadCredential(ctx, tx, a.OrganizationID, a.UserID)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !c.enabled {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.request.denied", resource, "active_mfa_required")
		}
		if e != nil {
			return e
		}
		now := s.now().UTC()
		result = BreakGlass{id: token, organizationID: a.OrganizationID, userID: a.UserID, resource: resource, permission: permission, reason: reason, generation: c.generation, version: 1, requestedAt: now, expiresAt: now.Add(duration)}
		if _, e = tx.ExecContext(ctx, `INSERT INTO extension_security_breakglass(id,organization_id,user_id,resource_id,permission,reason,credential_generation,requested_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, token, a.OrganizationID, a.UserID, resource, permission, reason, c.generation, now, result.expiresAt); e != nil {
			return e
		}
		return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.request", resource, reason)
	})
	if e != nil {
		return BreakGlass{}, e
	}
	if denied {
		return BreakGlass{}, ErrDenied
	}
	return result, nil
}
func (s *Service) ApproveBreakGlass(ctx context.Context, a ext.Actor, id string, expected uint64) (BreakGlass, error) {
	if !actorAllowed(a, BreakGlassApprovePermission) || !a.MFA || a.SessionID == "" {
		return BreakGlass{}, s.denyBreakGlass(ctx, a, "breakglass.approve", id)
	}
	var result BreakGlass
	var denied bool
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		approver, e := loadCredential(ctx, tx, a.OrganizationID, a.UserID)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !approver.enabled {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.approve.denied", id, "active_mfa_required")
		}
		if e != nil {
			return e
		}
		g, e := loadBreakGlass(ctx, tx, a.OrganizationID, id)
		if errors.Is(e, sql.ErrNoRows) {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.approve.denied", id, "unknown_grant")
		}
		if e != nil {
			return e
		}
		if g.userID == a.UserID || g.revokedAt.Valid || g.approvedBy != "" || !s.now().Before(g.expiresAt) || g.expiresAt.Sub(g.requestedAt) > s.maxBreakGlass || !s.allowed[g.permission] {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, g.userID, a.UserID, "breakglass.approve.denied", g.resource, "invalid_or_self_approval")
		}
		if g.version != expected {
			return ErrConflict
		}
		var generation uint64
		var enabled bool
		if e = tx.QueryRowContext(ctx, `SELECT generation,enabled FROM extension_security_mfa WHERE organization_id=$1 AND user_id=$2`, g.organizationID, g.userID).Scan(&generation, &enabled); e != nil {
			return e
		}
		if !enabled || generation != g.generation {
			denied = true
			return s.audit(ctx, tx, g.organizationID, g.userID, a.UserID, "breakglass.approve.denied", g.resource, "revoked_credential")
		}
		now := s.now().UTC()
		if _, e = tx.ExecContext(ctx, `UPDATE extension_security_breakglass SET approved_by=$1,approved_at=$2,version=version+1 WHERE id=$3 AND version=$4`, a.UserID, now, id, expected); e != nil {
			return e
		}
		g.approvedBy = a.UserID
		g.approvedAt = sql.NullTime{Time: now, Valid: true}
		g.version++
		result = g
		return s.audit(ctx, tx, g.organizationID, g.userID, a.UserID, "breakglass.approve", g.resource, g.reason)
	})
	if e != nil {
		return BreakGlass{}, e
	}
	if denied {
		return BreakGlass{}, ErrDenied
	}
	return result, nil
}
func (s *Service) AuthorizeBreakGlass(ctx context.Context, a ext.Actor, id, resource, permission string) error {
	if !actorAllowed(a, BreakGlassUsePermission) || !a.MFA || a.SessionID == "" {
		return s.denyBreakGlass(ctx, a, "breakglass.use", resource)
	}
	var denied bool
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := loadCredential(ctx, tx, a.OrganizationID, a.UserID)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !c.enabled {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.use.denied", resource, "active_mfa_required")
		}
		if e != nil {
			return e
		}
		g, e := loadBreakGlass(ctx, tx, a.OrganizationID, id)
		if errors.Is(e, sql.ErrNoRows) {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.use.denied", resource, "unknown_grant")
		}
		if e != nil {
			return e
		}
		if g.userID != a.UserID || g.resource != resource || g.permission != permission || !s.allowed[permission] || g.generation != c.generation || g.approvedBy == "" || !g.approvedAt.Valid || g.revokedAt.Valid || !s.now().Before(g.expiresAt) || g.expiresAt.Sub(g.requestedAt) > s.maxBreakGlass {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.use.denied", resource, "scope_expiry_or_revocation")
		}
		return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.use", resource, g.reason)
	})
	if e != nil {
		return e
	}
	if denied {
		return ErrDenied
	}
	return nil
}
func (s *Service) RevokeBreakGlass(ctx context.Context, a ext.Actor, id string, expected uint64) (BreakGlass, error) {
	if !actorAllowed(a, BreakGlassRevokePermission) || !a.MFA || a.SessionID == "" {
		return BreakGlass{}, s.denyBreakGlass(ctx, a, "breakglass.revoke", id)
	}
	var result BreakGlass
	var denied bool
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := loadCredential(ctx, tx, a.OrganizationID, a.UserID)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !c.enabled {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.revoke.denied", id, "active_mfa_required")
		}
		if e != nil {
			return e
		}
		g, e := loadBreakGlass(ctx, tx, a.OrganizationID, id)
		if errors.Is(e, sql.ErrNoRows) {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "breakglass.revoke.denied", id, "unknown_grant")
		}
		if e != nil {
			return e
		}
		if g.version != expected {
			return ErrConflict
		}
		now := s.now().UTC()
		if _, e = tx.ExecContext(ctx, `UPDATE extension_security_breakglass SET revoked_at=$1,version=version+1 WHERE id=$2 AND version=$3`, now, id, expected); e != nil {
			return e
		}
		g.revokedAt = sql.NullTime{Time: now, Valid: true}
		g.version++
		result = g
		return s.audit(ctx, tx, g.organizationID, g.userID, a.UserID, "breakglass.revoke", g.resource, "explicit_revocation")
	})
	if e != nil {
		return BreakGlass{}, e
	}
	if denied {
		return BreakGlass{}, ErrDenied
	}
	return result, nil
}
