package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"golang.org/x/crypto/bcrypt"
)

// LoginRequest represents login credentials
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=12"`
}

// RegisterRequest represents registration data
type RegisterRequest struct {
	Email          string    `json:"email" validate:"required,email"`
	Password       string    `json:"password" validate:"required,min=12"`
	FullName       string    `json:"full_name" validate:"required"`
	OrganizationID uuid.UUID `json:"organization_id" validate:"required"`
}

// CookieAuthResponse represents authentication response when tokens are in cookies.
// No tokens in the body — only the expiry hint and user object.
type CookieAuthResponse struct {
	ExpiresIn int         `json:"expires_in"`
	User      models.User `json:"user"`
}

// RefreshRequest represents token refresh request
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Login authenticates a user and returns tokens
func (a *App) Login(r *fastglue.Request) error {
	var req LoginRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	// Find user by email with role preloaded
	var user models.User
	if err := a.DB.Preload("Role").Where("email = ?", req.Email).First(&user).Error; err != nil {
		// Run dummy bcrypt to prevent timing-based account enumeration
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), []byte(req.Password))
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Invalid credentials", nil, "")
	}

	// Load permissions from cache
	if user.Role != nil && user.RoleID != nil {
		cachedPerms, err := a.GetRolePermissionsCached(*user.RoleID)
		if err == nil {
			permissions := make([]models.Permission, 0, len(cachedPerms))
			for _, p := range cachedPerms {
				for i := len(p) - 1; i >= 0; i-- {
					if p[i] == ':' {
						permissions = append(permissions, models.Permission{
							Resource: p[:i],
							Action:   p[i+1:],
						})
						break
					}
				}
			}
			user.Role.Permissions = permissions
		}
	}

	// Check if user is active
	if !user.IsActive {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Account is disabled", nil, "")
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Invalid credentials", nil, "")
	}

	// Generate tokens
	accessToken, err := a.generateAccessToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate access token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}

	refreshToken, err := a.generateRefreshToken(&user, accessToken)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}

	a.setAuthCookies(r, accessToken, refreshToken)

	return r.SendEnvelope(CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

// Register creates a new user in an existing organization
func (a *App) Register(r *fastglue.Request) error {
	var req RegisterRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if a.isSupportEmail(req.Email) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Support account is protected", nil, "support_protected")
	}

	if req.OrganizationID == uuid.Nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "organization_id is required", nil, "")
	}

	// Validate the organization exists
	var org models.Organization
	if err := a.DB.Where("id = ?", req.OrganizationID).First(&org).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Organization not found", nil, "")
	}

	// Get the org's default role
	var defaultRole models.CustomRole
	if err := a.DB.Where("organization_id = ? AND is_default = ?", req.OrganizationID, true).First(&defaultRole).Error; err != nil {
		if err := a.DB.Where("organization_id = ? AND name = ? AND is_system = ?", req.OrganizationID, "agent", true).First(&defaultRole).Error; err != nil {
			a.Log.Error("Failed to find default role", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to find default role", nil, "")
		}
	}

	// Check if email already exists
	var existingUser models.User
	if err := a.DB.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
		// User exists — verify password and add to this org
		if err := bcrypt.CompareHashAndPassword([]byte(existingUser.PasswordHash), []byte(req.Password)); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "An account with this email already exists. Please sign in and ask your organization admin to add you.", nil, "")
		}

		// Check if user account is disabled
		if !existingUser.IsActive {
			return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Account is disabled", nil, "")
		}

		// Check if already a member of this org
		var count int64
		a.DB.Model(&models.UserOrganization{}).
			Where("user_id = ? AND organization_id = ?", existingUser.ID, req.OrganizationID).
			Count(&count)
		if count > 0 {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "You are already a member of this organization", nil, "")
		}

		// Add as member with default role
		userOrg := models.UserOrganization{
			UserID:         existingUser.ID,
			OrganizationID: req.OrganizationID,
			RoleID:         &defaultRole.ID,
			IsDefault:      false,
		}
		if err := a.DB.Create(&userOrg).Error; err != nil {
			if handled, responseErr := a.sendPlanError(r, err); handled {
				return responseErr
			}
			a.Log.Error("Failed to add existing user to organization", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to join organization", nil, "")
		}

		a.Log.Info("Existing user joined organization", "user_id", existingUser.ID, "org_id", req.OrganizationID)

		// Set org context to the new org for token generation
		existingUser.OrganizationID = req.OrganizationID
		existingUser.Role = &defaultRole
		existingUser.RoleID = &defaultRole.ID

		accessToken, err := a.generateAccessToken(&existingUser)
		if err != nil {
			a.Log.Error("Failed to generate access token", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
		}
		refreshToken, err := a.generateRefreshToken(&existingUser, accessToken)
		if err != nil {
			a.Log.Error("Failed to generate refresh token", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
		}

		a.setAuthCookies(r, accessToken, refreshToken)

		return r.SendEnvelope(CookieAuthResponse{
			ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
			User:      existingUser,
		})
	}

	// New user — run dummy bcrypt to prevent timing-based account enumeration
	_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), []byte(req.Password))

	// Create account
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		a.Log.Error("Failed to hash password", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create account", nil, "")
	}

	tx := a.DB.Begin()
	if tx.Error != nil {
		a.Log.Error("Failed to begin transaction", "error", tx.Error)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create account", nil, "")
	}

	user := models.User{
		OrganizationID: req.OrganizationID,
		Email:          req.Email,
		PasswordHash:   string(hashedPassword),
		FullName:       req.FullName,
		RoleID:         &defaultRole.ID,
		IsActive:       true,
	}

	if err := tx.Create(&user).Error; err != nil {
		tx.Rollback()
		if handled, responseErr := a.sendPlanError(r, err); handled {
			return responseErr
		}
		a.Log.Error("Failed to create user", "error", err, "email", req.Email, "org_id", req.OrganizationID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create account", nil, "")
	}

	userOrg := models.UserOrganization{
		UserID:         user.ID,
		OrganizationID: req.OrganizationID,
		RoleID:         &defaultRole.ID,
		IsDefault:      true,
	}
	if err := tx.Create(&userOrg).Error; err != nil {
		tx.Rollback()
		if handled, responseErr := a.sendPlanError(r, err); handled {
			return responseErr
		}
		a.Log.Error("Failed to create user organization entry", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create account", nil, "")
	}

	if err := tx.Commit().Error; err != nil {
		a.Log.Error("Failed to commit transaction", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create account", nil, "")
	}

	a.Log.Info("Registration completed", "user_id", user.ID, "org_id", req.OrganizationID)

	user.Role = &defaultRole

	accessToken, err := a.generateAccessToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate access token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}
	refreshToken, err := a.generateRefreshToken(&user, accessToken)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}

	a.setAuthCookies(r, accessToken, refreshToken)

	return r.SendEnvelope(CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

// RefreshToken refreshes access token using refresh token with rotation.
// The old refresh token is invalidated (single-use) and a new one is issued.
func (a *App) RefreshToken(r *fastglue.Request) error {
	// Read refresh token from cookie first, fall back to JSON body.
	refreshTokenStr := string(r.RequestCtx.Request.Header.Cookie(cookieRefreshName))
	if refreshTokenStr == "" {
		var req RefreshRequest
		_ = r.Decode(&req, "json")
		refreshTokenStr = req.RefreshToken
	}
	if refreshTokenStr == "" {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Missing refresh token", nil, "")
	}

	claims, err := middleware.ParseToken(a.Config.JWT.Secret, refreshTokenStr, "refresh")
	if err != nil || claims.ID == "" {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Invalid refresh token", nil, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := middleware.ValidateSession(ctx, a.Redis, claims); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Refresh token has been revoked", nil, "")
	}
	// GETDEL consumes state atomically, including concurrent refresh requests.
	owner, err := a.Redis.GetDel(ctx, refreshTokenKey(claims.ID)).Result()
	if err != nil || owner != claims.UserID.String() {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Refresh token has been revoked", nil, "")
	}
	current, err := middleware.CurrentIdentity(a.DB, claims)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Account is disabled or unavailable", nil, "")
	}
	user := *current
	user.OrganizationID = claims.OrganizationID
	if err := a.Redis.Del(ctx, middleware.SessionKey(claims.SessionID)).Err(); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Session storage unavailable", nil, "")
	}

	// Preserve the authenticated epoch: rotation must never adopt a later
	// password-reset/disable epoch if revocation races with this request.
	accessToken, err := a.generateAccessToken(&user, claims.SessionVersion, claims.SessionFamily)
	if err != nil {
		if errors.Is(err, middleware.ErrSessionRevoked) {
			return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Session has been revoked", nil, "")
		}
		a.Log.Error("Failed to generate access token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}
	newRefreshToken, err := a.generateRefreshToken(&user, accessToken)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}

	a.setAuthCookies(r, accessToken, newRefreshToken)

	return r.SendEnvelope(CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

func (a *App) generateAccessToken(user *models.User, expectedVersions ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := middleware.SessionVersion(ctx, a.Redis, user.ID)
	if err != nil {
		return "", err
	}
	if len(expectedVersions) > 0 && version != expectedVersions[0] {
		return "", middleware.ErrSessionRevoked
	}
	// Check after reading the epoch. A credential change before this read is
	// detected here; a change afterwards invalidates the token's old epoch.
	current, err := middleware.CurrentIdentity(a.DB.WithContext(ctx), &middleware.JWTClaims{UserID: user.ID, OrganizationID: user.OrganizationID})
	if err != nil {
		return "", err
	}
	if current.AuthStamp != user.AuthStamp || current.PasswordHash != user.PasswordHash || current.SSOBinding != user.SSOBinding || current.SSOProviderID != user.SSOProviderID || current.SSOProvider != user.SSOProvider {
		return "", middleware.ErrSessionRevoked
	}
	sid := uuid.NewString()
	expiry := time.Duration(a.Config.JWT.RefreshExpiryDays) * 24 * time.Hour
	family := sid
	if len(expectedVersions) > 1 {
		family = expectedVersions[1]
		// Atomic family fence: logout cannot be crossed by creating a successor.
		created, err := a.Redis.Eval(ctx, `if redis.call("GET", KEYS[1]) ~= ARGV[1] then return 0 end; redis.call("SET", KEYS[2], ARGV[1], "PX", ARGV[2]); return 1`, []string{middleware.FamilyKey(family), middleware.SessionKey(sid)}, user.ID.String(), expiry.Milliseconds()).Int()
		if err != nil {
			return "", err
		}
		if created != 1 {
			return "", middleware.ErrSessionRevoked
		}
	} else {
		_, err := a.Redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, middleware.FamilyKey(family), user.ID.String(), expiry)
			pipe.Set(ctx, middleware.SessionKey(sid), user.ID.String(), expiry)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return a.signPurposeToken(user, "access", sid, version, uuid.NewString(), time.Duration(a.Config.JWT.AccessExpiryMins)*time.Minute, family)
}

func (a *App) signPurposeToken(user *models.User, purpose, sid, version, jti string, expiry time.Duration, families ...string) (string, error) {
	family := sid
	if len(families) > 0 {
		family = families[0]
	}
	claims := middleware.JWTClaims{UserID: user.ID, OrganizationID: user.OrganizationID, Email: user.Email, RoleID: user.RoleID, IsSuperAdmin: user.IsSuperAdmin, SessionID: sid, SessionVersion: version, SessionFamily: family, AuthStamp: user.AuthStamp,
		RegisteredClaims: jwt.RegisteredClaims{ID: jti, Subject: purpose, Audience: jwt.ClaimStrings{"whatomate:" + purpose}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)), IssuedAt: jwt.NewNumericDate(time.Now()), Issuer: "whatomate"}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(a.Config.JWT.Secret))
}

func (a *App) generateRefreshToken(user *models.User, accessToken string) (string, error) {
	access, err := middleware.ParseToken(a.Config.JWT.Secret, accessToken, "access")
	if err != nil {
		return "", err
	}
	jti := uuid.NewString()
	expiry := time.Duration(a.Config.JWT.RefreshExpiryDays) * 24 * time.Hour
	signed, err := a.signPurposeToken(user, "refresh", access.SessionID, access.SessionVersion, jti, expiry, access.SessionFamily)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := middleware.ValidateSession(ctx, a.Redis, access); err != nil {
		return "", err
	}
	if err := a.Redis.Set(ctx, refreshTokenKey(jti), user.ID.String(), expiry).Err(); err != nil {
		return "", err
	}
	return signed, nil
}

// refreshTokenKey returns the Redis key for a refresh token JTI.
func refreshTokenKey(jti string) string {
	return fmt.Sprintf("refresh:%s", jti)
}

// SwitchOrgRequest represents the request body for switching organization
type SwitchOrgRequest struct {
	OrganizationID uuid.UUID `json:"organization_id"`
}

// SwitchOrg generates new tokens for a different organization the user belongs to
func (a *App) SwitchOrg(r *fastglue.Request) error {
	userID, ok := r.RequestCtx.UserValue("user_id").(uuid.UUID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	var req SwitchOrgRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	if req.OrganizationID == uuid.Nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "organization_id is required", nil, "")
	}

	// Verify the organization exists
	var org models.Organization
	if err := a.DB.Where("id = ?", req.OrganizationID).First(&org).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Organization not found", nil, "")
	}

	// Get the user
	var user models.User
	if err := a.DB.Where("id = ?", userID).First(&user).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "User not found", nil, "")
	}

	// Super admins can switch to any org; others need membership
	if !user.IsSuperAdmin {
		var userOrg models.UserOrganization
		if err := a.DB.Where("user_id = ? AND organization_id = ?", userID, req.OrganizationID).First(&userOrg).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You are not a member of this organization", nil, "")
		}
		// Use the role from the user_organizations table for the target org
		if userOrg.RoleID != nil {
			user.RoleID = userOrg.RoleID
		}
	}

	// Set the target org on the user for token generation
	user.OrganizationID = req.OrganizationID

	// Preload role with permissions for the response
	if user.RoleID != nil {
		var role models.CustomRole
		if err := a.DB.Where("id = ?", *user.RoleID).First(&role).Error; err == nil {
			user.Role = &role
			cachedPerms, err := a.GetRolePermissionsCached(*user.RoleID)
			if err == nil {
				permissions := make([]models.Permission, 0, len(cachedPerms))
				for _, p := range cachedPerms {
					parts := splitPermission(p)
					if len(parts) == 2 {
						permissions = append(permissions, models.Permission{
							Resource: parts[0],
							Action:   parts[1],
						})
					}
				}
				user.Role.Permissions = permissions
			}
		}
	}

	// Generate new tokens with the target org
	var expectedVersions []string
	if claims, ok := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims); ok {
		if claims.AuthStamp != user.AuthStamp {
			return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Session has been revoked", nil, "")
		}
		expectedVersions = append(expectedVersions, claims.SessionVersion, claims.SessionFamily)
	}
	accessToken, err := a.generateAccessToken(&user, expectedVersions...)
	if err != nil {
		if errors.Is(err, middleware.ErrSessionRevoked) {
			return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Session has been revoked", nil, "")
		}
		a.Log.Error("Failed to generate access token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}

	refreshToken, err := a.generateRefreshToken(&user, accessToken)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}

	a.setAuthCookies(r, accessToken, refreshToken)

	return r.SendEnvelope(CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

// LogoutRequest represents logout request body
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout invalidates the user's refresh token
func (a *App) Logout(r *fastglue.Request) error {
	// Read refresh token from cookie first, fall back to body.
	refreshTokenStr := string(r.RequestCtx.Request.Header.Cookie(cookieRefreshName))
	if refreshTokenStr == "" {
		var req LogoutRequest
		_ = r.Decode(&req, "json")
		refreshTokenStr = req.RefreshToken
	}

	if refreshTokenStr != "" {
		claims, err := middleware.ParseToken(a.Config.JWT.Secret, refreshTokenStr, "refresh")
		if err == nil && claims.ID != "" {
			if a.Redis == nil {
				return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Session storage unavailable", nil, "")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.Redis.Del(ctx, refreshTokenKey(claims.ID), middleware.SessionKey(claims.SessionID), middleware.FamilyKey(claims.SessionFamily)).Err(); err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Failed to revoke session", nil, "")
			}
		}
	}

	a.clearAuthCookies(r)

	return r.SendEnvelope(map[string]string{"status": "logged_out"})
}

func generateSlug(name string) string {
	// Simple slug generation - in production, use a proper slugify library
	slug := ""
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			slug += string(c)
		} else if c >= 'A' && c <= 'Z' {
			slug += string(c + 32)
		} else if c == ' ' || c == '-' {
			slug += "-"
		}
	}
	return slug + "-" + uuid.New().String()[:8]
}

// GetWSToken returns a short-lived single-use JWT for WebSocket authentication.
// This is needed because httpOnly cookies cannot be read by JavaScript to pass
// as a query parameter to the WebSocket connection URL.
func (a *App) GetWSToken(r *fastglue.Request) error {
	userID, ok := r.RequestCtx.UserValue("user_id").(uuid.UUID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	orgID, ok := r.RequestCtx.UserValue("organization_id").(uuid.UUID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	user, err := middleware.CurrentIdentity(a.DB, &middleware.JWTClaims{UserID: userID, OrganizationID: orgID})
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Account unavailable", nil, "")
	}
	user.OrganizationID = orgID
	session, ok := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims)
	if !ok {
		access, err := a.generateAccessToken(user)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Session storage unavailable", nil, "")
		}
		session, err = middleware.ParseToken(a.Config.JWT.Secret, access, "access")
		if err != nil {
			return err
		}
	}
	if session.AuthStamp != user.AuthStamp {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Session has been revoked", nil, "")
	}
	jti := uuid.NewString()
	signed, err := a.signPurposeToken(user, "ws", session.SessionID, session.SessionVersion, jti, 30*time.Second, session.SessionFamily)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate token", nil, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Redis.Set(ctx, "auth:ws:"+jti, user.ID.String(), 30*time.Second).Err(); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Session storage unavailable", nil, "")
	}
	return r.SendEnvelope(map[string]string{"token": signed})
}
