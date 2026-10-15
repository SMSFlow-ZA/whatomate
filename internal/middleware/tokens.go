package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

const ContextKeyClaims = "auth_claims"

var ErrSessionRevoked = errors.New("session revoked")

// ParseToken enforces the issuer, destination, purpose and signing algorithm.
// Tokens issued before purpose separation must be replaced by signing in again.
func ParseToken(secret, encoded, purpose string) (*JWTClaims, error) {
	claims := &JWTClaims{}
	token, err := jwt.ParseWithClaims(encoded, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("whatomate"), jwt.WithAudience("whatomate:"+purpose), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return nil, err
	}
	if !token.Valid || claims.Subject != purpose || claims.UserID == uuid.Nil || claims.OrganizationID == uuid.Nil || claims.IssuedAt == nil || claims.SessionFamily == "" {
		return nil, jwt.ErrTokenInvalidClaims
	}
	return claims, nil
}

// CurrentIdentity rejects disabled/deleted accounts and stale organization access.
func CurrentIdentity(db *gorm.DB, claims *JWTClaims) (*models.User, error) {
	var user models.User
	if db == nil {
		return nil, errors.New("identity storage unavailable")
	}
	if err := db.Where("id = ? AND is_active = ?", claims.UserID, true).First(&user).Error; err != nil {
		return nil, err
	}
	if claims.SessionID != "" && claims.AuthStamp != user.AuthStamp {
		return nil, ErrSessionRevoked
	}
	var org models.Organization
	if err := db.Where("id = ?", claims.OrganizationID).First(&org).Error; err != nil {
		return nil, err
	}
	if user.OrganizationID != claims.OrganizationID && !user.IsSuperAdmin {
		var membership models.UserOrganization
		if err := db.Where("user_id = ? AND organization_id = ?", user.ID, claims.OrganizationID).First(&membership).Error; err != nil {
			return nil, err
		}
		user.RoleID = membership.RoleID
	}
	return &user, nil
}

func FamilyKey(id string) string { return "auth:family:" + id }

func SessionKey(id string) string        { return "auth:session:" + id }
func VersionKey(userID uuid.UUID) string { return "auth:version:" + userID.String() }

func SessionVersion(ctx context.Context, client *redis.Client, userID uuid.UUID) (string, error) {
	if client == nil {
		return "", errors.New("session storage unavailable")
	}
	value, err := client.Get(ctx, VersionKey(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

func ValidateSession(ctx context.Context, client *redis.Client, claims *JWTClaims) error {
	if client == nil || claims.SessionID == "" || claims.SessionFamily == "" {
		return errors.New("missing session")
	}
	owner, err := client.Get(ctx, SessionKey(claims.SessionID)).Result()
	if err != nil || owner != claims.UserID.String() {
		return errors.New("session expired or revoked")
	}
	familyOwner, err := client.Get(ctx, FamilyKey(claims.SessionFamily)).Result()
	if err != nil || familyOwner != claims.UserID.String() {
		return ErrSessionRevoked
	}
	version, err := SessionVersion(ctx, client, claims.UserID)
	if err != nil || version != claims.SessionVersion {
		return errors.New("session revoked")
	}
	return nil
}

// RevokeUserSessions changes one per-user version, without scanning session keys.
func RevokeUserSessions(ctx context.Context, client *redis.Client, userID uuid.UUID) error {
	if client == nil {
		return errors.New("session storage unavailable")
	}
	return client.Set(ctx, VersionKey(userID), uuid.NewString(), 0).Err()
}

func sessionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
