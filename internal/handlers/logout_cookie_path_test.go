package handlers

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func logoutCookieTestApp(t *testing.T) *App {
	t.Helper()
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set or unavailable")
	}
	return &App{Redis: rdb, Config: &config.Config{
		JWT: config.JWTConfig{Secret: testutil.TestJWTSecret, AccessExpiryMins: 15, RefreshExpiryDays: 7},
	}}
}

func logoutCookieTestTokens(t *testing.T, app *App, family string) (string, string, *middleware.JWTClaims) {
	t.Helper()
	user := &models.User{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: uuid.New()}
	sid, jti := uuid.NewString(), uuid.NewString()
	if family == "" {
		family = uuid.NewString()
	}
	access, err := app.signPurposeToken(user, "access", sid, "", uuid.NewString(), time.Hour, family)
	require.NoError(t, err)
	refresh, err := app.signPurposeToken(user, "refresh", sid, "", jti, time.Hour, family)
	require.NoError(t, err)
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, access, "access")
	require.NoError(t, err)
	keys := []string{middleware.SessionKey(sid), middleware.FamilyKey(family), refreshTokenKey(jti)}
	for _, key := range keys {
		require.NoError(t, app.Redis.Set(context.Background(), key, user.ID.String(), time.Hour).Err())
	}
	client := app.Redis
	t.Cleanup(func() { client.Del(context.Background(), keys...) })
	return access, refresh, claims
}

func TestLogoutBrowserCookiePathsRevokeSession(t *testing.T) {
	for _, path := range []string{"/api/auth/logout", "/api/auth/refresh/logout"} {
		t.Run(path, func(t *testing.T) {
			app := logoutCookieTestApp(t)
			access, refresh, claims := logoutCookieTestTokens(t, app, "")
			_, _, independent := logoutCookieTestTokens(t, app, "")
			// A refresh may already have produced another access token in this
			// family. Logging out must revoke that successor as well.
			successor := *claims
			successor.SessionID = uuid.NewString()
			successorKey := middleware.SessionKey(successor.SessionID)
			require.NoError(t, app.Redis.Set(context.Background(), successorKey, claims.UserID.String(), time.Hour).Err())
			t.Cleanup(func() { app.Redis.Del(context.Background(), successorKey) })
			login := testutil.NewJSONRequest(t, map[string]string{})
			app.setAuthCookies(login, access, refresh)
			header := http.Header{}
			login.RequestCtx.Response.Header.VisitAllCookie(func(_, value []byte) {
				header.Add("Set-Cookie", string(value))
			})
			jar, err := cookiejar.New(nil)
			require.NoError(t, err)
			loginURL, err := url.Parse("https://example.invalid/api/auth/login")
			require.NoError(t, err)
			jar.SetCookies(loginURL, (&http.Response{Header: header}).Cookies())
			logoutURL, err := url.Parse("https://example.invalid" + path)
			require.NoError(t, err)
			logout := testutil.NewJSONRequest(t, map[string]string{})
			logout.RequestCtx.Request.SetRequestURI(logoutURL.String())
			for _, cookie := range jar.Cookies(logoutURL) {
				logout.RequestCtx.Request.Header.SetCookie(cookie.Name, cookie.Value)
			}
			require.Equal(t, access, string(logout.RequestCtx.Request.Header.Cookie(cookieAccessName)))
			if path == "/api/auth/logout" {
				require.Empty(t, logout.RequestCtx.Request.Header.Cookie(cookieRefreshName))
			} else {
				require.Equal(t, refresh, string(logout.RequestCtx.Request.Header.Cookie(cookieRefreshName)))
			}
			require.NoError(t, middleware.ValidateSession(context.Background(), app.Redis, claims))
			require.NoError(t, middleware.ValidateSession(context.Background(), app.Redis, &successor))
			require.NoError(t, app.Logout(logout))
			require.Equal(t, 200, logout.RequestCtx.Response.StatusCode())
			require.Error(t, middleware.ValidateSession(context.Background(), app.Redis, claims))
			require.Error(t, middleware.ValidateSession(context.Background(), app.Redis, &successor))
			require.NoError(t, middleware.ValidateSession(context.Background(), app.Redis, independent))
			require.EqualValues(t, 0, app.Redis.Exists(context.Background(), middleware.FamilyKey(claims.SessionFamily)).Val())
		})
	}
}

func TestLogoutRefreshCookieDoesNotRevokeUnrelatedAccessFamily(t *testing.T) {
	app := logoutCookieTestApp(t)
	_, refresh, target := logoutCookieTestTokens(t, app, "")
	access, _, unrelated := logoutCookieTestTokens(t, app, "")
	logout := testutil.NewJSONRequest(t, map[string]string{})
	logout.RequestCtx.Request.Header.SetCookie(cookieRefreshName, refresh)
	logout.RequestCtx.Request.Header.SetCookie(cookieAccessName, access)
	require.NoError(t, app.Logout(logout))
	require.Equal(t, 200, logout.RequestCtx.Response.StatusCode())
	require.Error(t, middleware.ValidateSession(context.Background(), app.Redis, target))
	require.NoError(t, middleware.ValidateSession(context.Background(), app.Redis, unrelated))
}

func TestLogoutAccessCookieCannotUseForgedOrWrongPurposeToken(t *testing.T) {
	for _, kind := range []string{"forged", "refresh", "expired", "missing-session"} {
		t.Run(kind, func(t *testing.T) {
			app := logoutCookieTestApp(t)
			_, refresh, claims := logoutCookieTestTokens(t, app, "")
			token := refresh
			if kind != "refresh" {
				signer := App{}
				cfg := *app.Config
				signer.Config = &cfg
				expiry := time.Hour
				sid := claims.SessionID
				if kind == "forged" {
					signer.Config.JWT.Secret = "untrusted-test-signing-key"
				} else if kind == "expired" {
					expiry = -time.Hour
				} else {
					sid = ""
				}
				var err error
				token, err = signer.signPurposeToken(&models.User{BaseModel: models.BaseModel{ID: claims.UserID}, OrganizationID: claims.OrganizationID}, "access", sid, "", uuid.NewString(), expiry, claims.SessionFamily)
				require.NoError(t, err)
			}
			logout := testutil.NewJSONRequest(t, map[string]string{})
			logout.RequestCtx.Request.Header.SetCookie(cookieAccessName, token)
			require.NoError(t, app.Logout(logout))
			require.Equal(t, 200, logout.RequestCtx.Response.StatusCode())
			require.NoError(t, middleware.ValidateSession(context.Background(), app.Redis, claims))
		})
	}
}

func TestLogoutAccessCookieRequiresSessionStorage(t *testing.T) {
	app := logoutCookieTestApp(t)
	access, _, _ := logoutCookieTestTokens(t, app, "")
	app.Redis = nil
	logout := testutil.NewJSONRequest(t, map[string]string{})
	logout.RequestCtx.Request.Header.SetCookie(cookieAccessName, access)
	require.NoError(t, app.Logout(logout))
	require.Equal(t, 503, logout.RequestCtx.Response.StatusCode())
}
