package handlers

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestSecurityTokens_PurposeStateAndSessionRevocation(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	refresh, err := app.generateRefreshToken(user, access)
	require.NoError(t, err)
	for _, encoded := range []string{access, refresh} {
		_, _, err := app.validateWSTokenFn()(encoded)
		require.Error(t, err, "only websocket tokens authenticate a socket")
	}
	wsreq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(wsreq, org.ID, user.ID)
	require.NoError(t, app.GetWSToken(wsreq))
	var response struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wsreq.RequestCtx.Response.Body(), &response))
	wsToken := response.Data["token"]
	_, _, err = app.validateWSTokenFn()(wsToken)
	require.NoError(t, err)
	_, _, err = app.validateWSTokenFn()(wsToken)
	require.Error(t, err, "websocket token is single use")
	for _, purpose := range []string{"access", "refresh"} {
		_, err := middleware.ParseToken(app.Config.JWT.Secret, wsToken, purpose)
		require.Error(t, err)
	}
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, refresh, "refresh")
	require.NoError(t, err)
	for _, change := range []string{"missing-jti", "wrong-issuer", "wrong-audience", "wrong-method", "missing-session", "unknown-jti"} {
		t.Run(change, func(t *testing.T) {
			changed := *claims
			method := jwt.SigningMethodHS256
			switch change {
			case "missing-jti":
				changed.ID = ""
			case "wrong-issuer":
				changed.Issuer = "other"
			case "wrong-audience":
				changed.Audience = jwt.ClaimStrings{"other"}
			case "wrong-method":
				method = jwt.SigningMethodHS384
			case "missing-session":
				changed.SessionID = ""
			case "unknown-jti":
				changed.ID = uuid.NewString()
			}
			encoded, err := jwt.NewWithClaims(method, changed).SignedString([]byte(app.Config.JWT.Secret))
			require.NoError(t, err)
			req := testutil.NewJSONRequest(t, map[string]string{"refresh_token": encoded})
			require.NoError(t, app.RefreshToken(req))
			require.Equal(t, 401, req.RequestCtx.Response.StatusCode())
		})
	}
	// Revoking this refresh session also invalidates its access token.
	logout := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.Logout(logout))
	req := testutil.NewGETRequest(t)
	req.RequestCtx.Request.Header.Set("Authorization", "Bearer "+access)
	require.Nil(t, middleware.AuthWithDB(app.Config.JWT.Secret, app.DB, app.Redis)(req))
}

func TestSecurityTokens_DisableAndStorageFailure(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, access, "access")
	require.NoError(t, err)
	wsToken, err := app.signPurposeToken(user, "ws", claims.SessionID, claims.SessionVersion, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(user).Update("is_active", false).Error)
	_, _, err = app.validateWSTokenFn()(wsToken)
	require.Error(t, err)
	require.NoError(t, app.DB.Model(user).Update("is_active", true).Error)
	require.NoError(t, middleware.RevokeUserSessions(context.Background(), app.Redis, user.ID))
	require.Error(t, middleware.ValidateSession(context.Background(), app.Redis, claims))
	app.Redis = nil
	_, err = app.generateAccessToken(user)
	require.Error(t, err, "session creation fails closed without Redis")
	require.Error(t, middleware.ValidateSession(context.Background(), nil, claims))
}

func TestSecurityTokens_EstablishedSocketClosesOnDisable(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, access, "access")
	require.NoError(t, err)
	closed := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	go app.watchWSSession(done, claims, time.Millisecond, func() { close(closed) })
	require.NoError(t, app.DB.Model(user).Update("is_active", false).Error)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("established socket kept disabled account connected")
	}
}

func TestSecurityTokens_DisableRevokesSessionsAcrossReenable(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	require.True(t, app.HasPermission(user.ID, "users", "write", org.ID))
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	refresh, err := app.generateRefreshToken(user, access)
	require.NoError(t, err)
	for _, active := range []bool{false, true} {
		req := testutil.NewJSONRequest(t, map[string]bool{"is_active": active})
		testutil.SetAuthContext(req, org.ID, admin.ID)
		testutil.SetPathParam(req, "id", user.ID.String())
		require.NoError(t, app.UpdateUser(req))
		require.Equal(t, 200, req.RequestCtx.Response.StatusCode())
	}
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, access, "access")
	require.NoError(t, err)
	require.Error(t, middleware.ValidateSession(context.Background(), app.Redis, claims))
	req := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.RefreshToken(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode())
}

func TestSecurityTokens_ConcurrentRefreshHasOneWinner(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	refresh, err := app.generateRefreshToken(user, access)
	require.NoError(t, err)
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		go func() {
			req := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
			_ = app.RefreshToken(req)
			statuses <- req.RequestCtx.Response.StatusCode()
		}()
	}
	successes := 0
	for i := 0; i < 8; i++ {
		status := <-statuses
		if status == 200 {
			successes++
		} else {
			require.Equal(t, 401, status)
		}
	}
	require.Equal(t, 1, successes)
}

func TestSecurityTokens_UnavailableRedisFailsClosed(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	refresh, err := app.generateRefreshToken(user, access)
	require.NoError(t, err)
	unavailable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 20 * time.Millisecond, ContextTimeoutEnabled: true})
	defer func() { require.NoError(t, unavailable.Close()) }()
	app.Redis = unavailable
	_, err = app.generateAccessToken(user)
	require.Error(t, err)
	req := testutil.NewGETRequest(t)
	req.RequestCtx.Request.Header.Set("Authorization", "Bearer "+access)
	require.Nil(t, middleware.AuthWithDB(app.Config.JWT.Secret, app.DB, unavailable)(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode())
	req = testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.RefreshToken(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode())
	req = testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.Logout(req))
	require.Equal(t, 503, req.RequestCtx.Response.StatusCode())
}

func TestSecurityTokens_RefreshCannotCrossRevocation(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	refresh, err := app.generateRefreshToken(user, access)
	require.NoError(t, err)
	var once sync.Once
	callbackName := "security_revoke_during_refresh"
	require.NoError(t, app.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			once.Do(func() { require.NoError(t, middleware.RevokeUserSessions(context.Background(), app.Redis, user.ID)) })
		}
	}))
	defer func() { require.NoError(t, app.DB.Callback().Query().Remove(callbackName)) }()
	req := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.RefreshToken(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode(), "in-flight refresh must not adopt a newer revocation epoch")
}

func TestSecurityTokens_ChangedCredentialsCannotMintNewSession(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(user).Update("password_hash", "changed-password-hash").Error)
	// Keep the already-authenticated credential snapshot from before the reset.
	user.PasswordHash = "old-password-hash"
	_, err := app.generateAccessToken(user)
	require.Error(t, err)
}
