package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errRegistrationMember = errors.New("already an organization member")

// Policy-based provisioning has no authenticated administrator actor. Successful
// session creation is observed separately, after both tokens have been created.
func (a *App) recordIdentityProvisioning(r *fastglue.Request, tx *gorm.DB, org, target uuid.UUID, reason audit.Reason) error {
	return audit.RecordSecurity(tx, audit.SecurityEvent{OrganizationID: org, ResourceType: "user", ResourceID: target,
		Kind: audit.IdentityProvisioned, Outcome: audit.Success, Reason: reason, Context: a.requestAuditContext(r)})
}

func registrationRole(tx *gorm.DB, org uuid.UUID) (models.CustomRole, error) {
	var organization models.Organization
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&organization, "id=?", org).Error; err != nil {
		return models.CustomRole{}, err
	}
	var role models.CustomRole
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("organization_id=? AND is_default", org).Order("id").First(&role).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&role, "organization_id=? AND name='agent' AND is_system", org).Error
	}
	return role, err
}

func (a *App) provisionRegisteredIdentity(r *fastglue.Request, user *models.User, org uuid.UUID, existing bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if existing {
			var current models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=?", user.ID).Error; err != nil {
				return err
			}
			// The password was verified before this lock. Do not cross a reset,
			// disable or identity change using a stale credential snapshot.
			if !current.IsActive || current.AuthStamp != user.AuthStamp || current.PasswordHash != user.PasswordHash || current.Email != user.Email {
				return errAuditUserChanged
			}
			var count int64
			if err := tx.Unscoped().Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", user.ID, org).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return errRegistrationMember // Revoked memberships require an administrator.
			}
			*user = current
		}
		role, err := registrationRole(tx, org)
		if err != nil {
			return err
		}
		if !existing {
			user.OrganizationID, user.RoleID = org, &role.ID
			if err := tx.Omit(clause.Associations).Create(user).Error; err != nil {
				return err
			}
		}
		membership := models.UserOrganization{UserID: user.ID, OrganizationID: org, RoleID: &role.ID, IsDefault: !existing}
		if err := tx.Create(&membership).Error; err != nil {
			return err
		}
		reason := audit.RegistrationCreated
		if existing {
			reason = audit.RegistrationJoined
		}
		if err := a.recordIdentityProvisioning(r, tx, org, user.ID, reason); err != nil {
			return err
		}
		// Only the response/session context changes for a cross-org join. The
		// existing user's native organisation and role stay unchanged in storage.
		user.OrganizationID, user.RoleID, user.Role = org, &role.ID, &role
		return nil
	})
}

func (a *App) registerWithAudit(r *fastglue.Request) error {
	var req RegisterRequest
	if a.decodeRequest(r, &req) != nil {
		return nil
	}
	if a.isSupportEmail(req.Email) {
		return r.SendErrorEnvelope(403, "Support account is protected", nil, "support_protected")
	}
	if req.OrganizationID == uuid.Nil {
		return r.SendErrorEnvelope(400, "organization_id is required", nil, "")
	}
	var org models.Organization
	if err := a.DB.Select("id").First(&org, "id=?", req.OrganizationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.SendErrorEnvelope(404, "Organization not found", nil, "")
		}
		return r.SendErrorEnvelope(503, "Registration is unavailable", nil, "")
	}
	var user models.User
	err := a.DB.Where("email=?", req.Email).First(&user).Error
	existing := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return r.SendErrorEnvelope(503, "Registration is unavailable", nil, "")
	}
	if existing {
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
			return r.SendErrorEnvelope(409, "An account with this email already exists. Please sign in and ask your organization admin to add you.", nil, "")
		}
		if !user.IsActive {
			return r.SendErrorEnvelope(401, "Account is disabled", nil, "")
		}
	} else {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), []byte(req.Password))
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			return r.SendErrorEnvelope(500, "Failed to create account", nil, "")
		}
		user = models.User{Email: req.Email, PasswordHash: string(hash), FullName: req.FullName, IsActive: true}
	}
	if err := a.provisionRegisteredIdentity(r, &user, req.OrganizationID, existing); err != nil {
		if handled, responseErr := a.sendPlanError(r, err); handled {
			return responseErr
		}
		if errors.Is(err, errRegistrationMember) {
			return r.SendErrorEnvelope(409, "You are already a member or need administrator approval to rejoin", nil, "")
		}
		if errors.Is(err, errAuditUserChanged) {
			return r.SendErrorEnvelope(409, "Account changed; sign in again before joining", nil, "")
		}
		a.Log.Error("Registration provisioning failed", "code", audit.ErrorCode(err))
		return r.SendErrorEnvelope(500, "Failed to create account", nil, "")
	}
	accessToken, err := a.generateAccessToken(&user)
	if err != nil {
		a.observeSignIn(r, user.OrganizationID, user.ID, uuid.Nil, audit.Failed, audit.SessionCreationFailed)
		a.Log.Error("Registration session failed", "code", audit.ErrorCode(err))
		return r.SendErrorEnvelope(500, "Failed to generate token", nil, "")
	}
	refreshToken, err := a.generateRefreshToken(&user, accessToken)
	if err != nil {
		a.observeSignIn(r, user.OrganizationID, user.ID, uuid.Nil, audit.Failed, audit.SessionCreationFailed)
		a.Log.Error("Registration refresh session failed", "code", audit.ErrorCode(err))
		return r.SendErrorEnvelope(500, "Failed to generate token", nil, "")
	}
	a.setAuthCookies(r, accessToken, refreshToken)
	a.observeSignIn(r, user.OrganizationID, user.ID, user.ID, audit.Success, audit.Password)
	return r.SendEnvelope(CookieAuthResponse{ExpiresIn: a.Config.JWT.AccessExpiryMins * 60, User: user})
}

func (a *App) provisionSSOIdentity(r *fastglue.Request, user *models.User, snapshot models.SSOProvider) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.SSOProvider
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&current, "id=? AND organization_id=?", snapshot.ID, snapshot.OrganizationID).Error; err != nil {
			return err
		}
		// Endpoints, role, domain and auto-create policy may have changed while
		// exchanging the code. No provider network call occurs under this lock.
		if !current.IsEnabled || !current.AllowAutoCreate || !current.UpdatedAt.Equal(snapshot.UpdatedAt) || SSOProviderBinding(&current) != SSOProviderBinding(&snapshot) {
			return errAuditUserChanged
		}
		if user.SSOProvider != current.Provider || user.SSOProviderID == "" || user.SSOBinding != SSOProviderBinding(&current) {
			return errAuditUserChanged
		}
		var org models.Organization
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&org, "id=?", current.OrganizationID).Error; err != nil {
			return err
		}
		roleName := current.DefaultRoleName
		if roleName == "" {
			roleName = "agent"
		}
		var role models.CustomRole
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&role, "organization_id=? AND name=?", current.OrganizationID, roleName).Error; err != nil {
			return err
		}
		user.OrganizationID, user.RoleID = current.OrganizationID, &role.ID
		if err := tx.Omit(clause.Associations).Create(user).Error; err != nil {
			return err
		}
		membership := models.UserOrganization{UserID: user.ID, OrganizationID: current.OrganizationID, RoleID: &role.ID, IsDefault: true}
		if err := tx.Create(&membership).Error; err != nil {
			return err
		}
		return a.recordIdentityProvisioning(r, tx, current.OrganizationID, user.ID, audit.SSOProvisioned)
	})
}
