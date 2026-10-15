package main

import (
	"encoding/json"
	websocketclient "github.com/fasthttp/websocket"
	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func securityRouter(t *testing.T) (*handlers.App, *fastglue.Fastglue) {
	t.Helper()
	app := &handlers.App{DB: testutil.SetupTestDB(t), Redis: testutil.SetupTestRedis(t), Log: testutil.NopLogger(), Config: &config.Config{JWT: config.JWTConfig{Secret: testutil.TestJWTSecret, AccessExpiryMins: 15, RefreshExpiryDays: 7}}}
	require.NotNil(t, app.Redis)
	g := fastglue.New()
	setupRoutes(g, app, app.Log, "", app.Redis, app.Config)
	return app, g
}

func securityLogin(t *testing.T, app *handlers.App, user *models.User) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]string{"email": user.Email, "password": "security-password-123"})
	require.NoError(t, app.Login(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
	return r
}

func TestSecurityRouter_RestrictedRolesCannotAdminister(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	login := securityLogin(t, app, user)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/roles", `{"name":"escalated","permissions":["users:write"]}`},
		{"PUT", "/api/roles/" + role.ID.String(), `{"name":"escalated","permissions":["users:write"]}`},
		{"GET", "/api/roles", ``}, {"GET", "/api/permissions", ``},
		{"POST", "/api/webhooks", `{"name":"exfiltration","url":"https://example.test/events","events":["message.received"]}`},
		{"GET", "/api/webhooks", ``},
		{"PUT", "/api/settings/sso/custom", `{"client_id":"attacker","is_enabled":true,"auth_url":"https://example.test/auth","token_url":"https://example.test/token","user_info_url":"https://example.test/info"}`},
		{"GET", "/api/settings/sso", ``},
		{"POST", "/api/custom-actions", `{"name":"injected","action_type":"url","config":{"url":"https://example.test"}}`},
		{"GET", "/api/custom-actions", ``},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod(tc.method)
			ctx.Request.SetRequestURI(tc.path)
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.SetBodyString(tc.body)
			ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
			g.Handler()(ctx)
			require.Equal(t, 403, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		})
	}
}

func TestSecurityRouter_BrowserLogoutRevokesRefresh(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithPassword("security-password-123"))
	login := securityLogin(t, app, user)
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("http://example.test/api/auth/login")
	login.RequestCtx.Response.Header.VisitAllCookie(func(_, val []byte) {
		h := http.Header{}
		h.Add("Set-Cookie", string(val))
		jar.SetCookies(origin, (&http.Response{Header: h}).Cookies())
	})
	logoutURL, _ := url.Parse("http://example.test/api/auth/refresh/logout")
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI(logoutURL.String())
	for _, c := range jar.Cookies(logoutURL) {
		ctx.Request.Header.SetCookie(c.Name, c.Value)
	}
	g.Handler()(ctx)
	require.Equal(t, 200, ctx.Response.StatusCode())
	req := testutil.NewJSONRequest(t, map[string]string{"refresh_token": testutil.GetResponseCookie(login, "whm_refresh")})
	require.NoError(t, app.RefreshToken(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode())
}

func TestSecurityRouter_DisabledJWTAndPurposeSubstitution(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithPassword("security-password-123"))
	login := securityLogin(t, app, user)
	refresh := testutil.GetResponseCookie(login, "whm_refresh")
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/me")
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.Header.Set("Authorization", "Bearer "+refresh)
	g.Handler()(ctx)
	require.Equal(t, 401, ctx.Response.StatusCode(), "refresh cannot authorize API")
	req := testutil.NewJSONRequest(t, map[string]string{"refresh_token": testutil.GetResponseCookie(login, "whm_access")})
	require.NoError(t, app.RefreshToken(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode(), "access cannot refresh")
	require.NoError(t, app.DB.Model(user).Update("is_active", false).Error)
	ctx = &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/me")
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
	g.Handler()(ctx)
	require.Equal(t, 401, ctx.Response.StatusCode(), "disabled account cannot use JWT")
}

func TestSecurityRouter_AllFamiliesDenyMissingPermissions(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRole(t, app.DB, org.ID, "restricted", nil)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	login := securityLogin(t, app, user)
	for _, path := range []string{"users", "accounts", "templates", "flows", "campaigns", "chatbot/settings", "chatbot/keywords", "chatbot/flows", "chatbot/ai-contexts", "chatbot/sessions", "chatbot/transfers", "teams", "audit-logs", "canned-responses", "analytics/dashboard", "analytics/agents", "widgets", "org/settings", "organizations", "tags", "ivr-flows", "call-logs", "call-transfers", "calls/ice-servers", "catalogs", "products/00000000-0000-0000-0000-000000000001", "contacts", "messages", "api-keys", "webhooks", "roles", "permissions", "custom-actions", "settings/sso", "unknown-family"} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(method+path, func(t *testing.T) {
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.SetRequestURI("/api/" + path)
				ctx.Request.Header.SetMethod(method)
				ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
				g.Handler()(ctx)
				// Unregistered methods are rejected by the router before middleware.
				require.Contains(t, []int{403, 405}, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			})
		}
	}
}

func TestSecurityRouter_AdminAndSupportAllowed(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	for _, support := range []bool{false, true} {
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"), func(u *models.User) { u.IsSuperAdmin = support })
		login := securityLogin(t, app, user)
		for _, path := range []string{"roles", "permissions", "webhooks", "custom-actions", "settings/sso"} {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/api/" + path)
			ctx.Request.Header.SetMethod("GET")
			ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
			g.Handler()(ctx)
			require.Equal(t, 200, ctx.Response.StatusCode(), path)
		}
	}
}

func TestSecurityRouter_CampaignWriteCannotExecute(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "editor", []string{"campaigns:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	login := securityLogin(t, app, user)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/campaigns/00000000-0000-0000-0000-000000000001/start")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
	g.Handler()(ctx)
	require.Equal(t, 403, ctx.Response.StatusCode())
}

func TestSecurityRouter_QueueAgentReceivesOnlyPickupFlag(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "queue-agent", []string{"transfers:read", "transfers:pickup"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	login := securityLogin(t, app, user)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{OrganizationID: org.ID, AI: models.AIConfig{SystemPrompt: "private-prompt"}, AgentAssignment: models.AgentAssignmentConfig{AllowQueuePickup: false}}).Error)
	require.NoError(t, app.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ?", org.ID).Update("allow_agent_queue_pickup", false).Error)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/chatbot/settings")
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
	g.Handler()(ctx)
	require.Equal(t, 200, ctx.Response.StatusCode())
	require.JSONEq(t, `{"status":"success","data":{"settings":{"allow_agent_queue_pickup":false}}}`, string(ctx.Response.Body()))
}

func TestSecurityRouter_SessionTicketAuthenticatesRealWebSocket(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithPassword("security-password-123"))
	login := securityLogin(t, app, user)
	app.WSHub = ws.NewHub(app.Log)
	go app.WSHub.Run()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &fasthttp.Server{Handler: g.Handler()}
	go server.Serve(listener)
	defer server.Shutdown()
	request := &fasthttp.RequestCtx{}
	request.Request.Header.SetMethod("GET")
	request.Request.SetRequestURI("/api/auth/ws-token")
	request.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
	g.Handler()(request)
	require.Equal(t, 200, request.Response.StatusCode())
	var response struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(request.Response.Body(), &response))
	conn, _, err := websocketclient.DefaultDialer.Dial("ws://"+listener.Addr().String()+"/ws", nil)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "auth", "payload": map[string]string{"token": response.Data.Token}}))
	require.NoError(t, conn.WriteJSON(map[string]string{"type": "ping"}))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, body, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Contains(t, string(body), `"type":"pong"`)
}

func TestSecurityRouter_PickupAgentResumesOnlyOwnTransfer(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "pickup-only", []string{"transfers:pickup"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	other := testutil.CreateTestUser(t, app.DB, org.ID)
	login := securityLogin(t, app, user)
	for _, owner := range []*models.User{user, other} {
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		transfer := models.AgentTransfer{OrganizationID: org.ID, ContactID: contact.ID, AgentID: &owner.ID, Status: models.TransferStatusActive}
		require.NoError(t, app.DB.Create(&transfer).Error)
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/chatbot/transfers/" + transfer.ID.String() + "/resume")
		ctx.Request.Header.SetMethod("PUT")
		ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
		g.Handler()(ctx)
		expected := 403
		if owner.ID == user.ID {
			expected = 200
		}
		require.Equal(t, expected, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
}
