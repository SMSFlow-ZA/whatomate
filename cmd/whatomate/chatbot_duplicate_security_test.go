package main

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestSecurityRouter_ChatbotDuplicateRequiresSessionCSRFAndPermissions(t *testing.T) {
	app, g := securityRouter(t)
	// runServer installs the global CSRF guard before setupRoutes; the shared
	// unit router helper only installs routes. Include the server guard here.
	g.Before(middleware.CSRFProtection())
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	flow := models.ChatbotFlow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Router source", IsEnabled: true}
	require.NoError(t, app.DB.Create(&flow).Error)
	login := securityLogin(t, app, user)
	for _, csrf := range []bool{false, true} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("POST")
		ctx.Request.SetRequestURI("/api/chatbot/flows/" + flow.ID.String() + "/duplicate")
		ctx.Request.Header.SetCookie("whm_access", testutil.GetResponseCookie(login, "whm_access"))
		if csrf {
			ctx.Request.Header.SetCookie("whm_csrf", "fixture-csrf")
			ctx.Request.Header.Set("X-CSRF-Token", "fixture-csrf")
		}
		g.Handler()(ctx)
		status := 403
		if csrf {
			status = 200
		}
		require.Equal(t, status, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
}
