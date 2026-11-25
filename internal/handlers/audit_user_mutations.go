package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errAuditUserChanged = errors.New("user security state changed")

func (a *App) recordUserSecurity(r *fastglue.Request, tx *gorm.DB, org, actor, target uuid.UUID, kind audit.EventKind, reason audit.Reason) error {
	return audit.RecordSecurity(tx, audit.SecurityEvent{OrganizationID: org, ActorID: actor, ResourceID: target, ResourceType: "user", Kind: kind, Outcome: audit.Success, Reason: reason, Context: a.requestAuditContext(r)})
}

// Redis cannot participate in the database transaction. Observe the actual
// pre-update fence separately from the subsequently committed security change.
func (a *App) fenceUserSecurityUpdate(r *fastglue.Request, ctx context.Context, org, actor, target uuid.UUID) error {
	err := middleware.RevokeUserSessions(ctx, a.Redis, target)
	event := audit.SecurityEvent{OrganizationID: org, ActorID: actor, ResourceID: target, ResourceType: "user", Kind: audit.SessionsRevoked, Outcome: audit.Success, Reason: audit.BeforeSecurityUpdate}
	if err != nil {
		event.Outcome, event.Reason = audit.Failed, audit.SessionStorageUnavailable
	}
	a.observeSecurity(r, event)
	return err
}

func userSecurityUpdateReason(req UserRequest, oldActive bool, self bool) audit.Reason {
	count := 0
	if req.Password != "" {
		count++
	}
	if req.IsActive != nil {
		count++
	}
	if req.SSOConfigID != nil {
		count++
	}
	if count == 1 {
		if req.Password != "" {
			if self {
				return audit.PasswordChanged
			}
			return audit.PasswordReset
		}
		if req.SSOConfigID != nil {
			return audit.SSOIdentityChanged
		}
		if req.IsActive != nil && *req.IsActive != oldActive {
			if *req.IsActive {
				return audit.UserEnabled
			}
			return audit.UserDisabled
		}
	}
	return audit.SecuritySettingsChanged
}

func sameAuditRole(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (a *App) updateMembershipAuthority(r *fastglue.Request, org, actor, target, roleID uuid.UUID) (*uuid.UUID, error) {
	var previous *uuid.UUID
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var role models.CustomRole
		if err := tx.Select("id").First(&role, "id=? AND organization_id=?", roleID, org).Error; err != nil {
			return err
		}
		var membership models.UserOrganization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&membership, "user_id=? AND organization_id=?", target, org).Error; err != nil {
			return err
		}
		if membership.RoleID != nil {
			id := *membership.RoleID
			previous = &id
		}
		if sameAuditRole(previous, &roleID) {
			return nil
		}
		if err := tx.Model(&membership).Update("role_id", roleID).Error; err != nil {
			return err
		}
		return a.recordUserSecurity(r, tx, org, actor, target, audit.PermissionChanged, audit.UserRoleChanged)
	})
	return previous, err
}

func (a *App) removeMembershipAuthority(r *fastglue.Request, org, actor, target uuid.UUID) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		var membership models.UserOrganization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&membership, "user_id=? AND organization_id=?", target, org).Error; err != nil {
			return err
		}
		if err := tx.Delete(&membership).Error; err != nil {
			return err
		}
		if err := a.recordUserSecurity(r, tx, org, actor, target, audit.PermissionChanged, audit.MembershipRemoved); err != nil {
			return err
		}
		return a.recordUserSecurity(r, tx, org, actor, target, audit.SessionsRevoked, audit.MembershipRemoved)
	})
}

// The caller holds the target membership/user lock. No credential material is
// accepted by this recorder; configuration diffs retain their separate purpose.
func (a *App) recordUserAuthorityChange(r *fastglue.Request, tx *gorm.DB, org, actor uuid.UUID, before, after models.User) error {
	if sameAuditRole(before.RoleID, after.RoleID) && before.IsSuperAdmin == after.IsSuperAdmin {
		return nil
	}
	reason := audit.UserRoleChanged
	if before.IsSuperAdmin != after.IsSuperAdmin {
		reason = audit.UserAuthorityChanged
	}
	return a.recordUserSecurity(r, tx, org, actor, after.ID, audit.PermissionChanged, reason)
}
