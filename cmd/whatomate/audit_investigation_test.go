package main

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestSecurityRouter_AuditInvestigationAndExportUseFreshScopedAuthority(t *testing.T) {
	app, g := securityRouter(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "audit-exporter", []string{"audit_logs:read", "audit_logs:export", "contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithPassword("security-password-123"))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	own := models.AuditLog{OrganizationID: org.ID, ResourceType: "contact", ResourceID: contact.ID, UserID: user.ID, UserName: "Synthetic own operator", Action: models.AuditActionUpdated, CreatedAt: time.Now().Add(-time.Hour)}
	require.NoError(t, app.DB.Create(&own).Error)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestContact(t, app.DB, otherOrg.ID)
	foreign := models.AuditLog{OrganizationID: otherOrg.ID, ResourceType: "contact", ResourceID: other.ID, UserID: user.ID, UserName: "Hidden foreign audit operator", Action: models.AuditActionUpdated}
	require.NoError(t, app.DB.Create(&foreign).Error)
	login := securityLogin(t, app, user)
	call := func(method, path string) *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod(method)
		ctx.Request.SetRequestURI(path)
		ctx.Request.Header.Set("Authorization", "Bearer "+testutil.GetResponseCookie(login, "whm_access"))
		g.Handler()(ctx)
		return ctx
	}
	list := call("GET", "/api/audit-logs?resource_type=contact&limit=1")
	require.Equal(t, 200, list.Response.StatusCode(), string(list.Response.Body()))
	require.Contains(t, string(list.Response.Body()), own.ID.String())
	require.NotContains(t, string(list.Response.Body()), foreign.ID.String())
	choices := call("GET", "/api/audit-logs/filters?resource_type=contact")
	require.Equal(t, 200, choices.Response.StatusCode())
	require.NotContains(t, string(choices.Response.Body()), "Hidden foreign")
	detail := call("GET", "/api/audit-logs/"+foreign.ID.String())
	require.Equal(t, 404, detail.Response.StatusCode())
	require.True(t, app.HasPermission(user.ID, models.ResourceAuditLogs, models.ActionExport, org.ID), "prime the older permission cache")
	export := call("POST", "/api/audit-logs/export?resource_type=contact&limit=1")
	require.Equal(t, 200, export.Response.StatusCode(), string(export.Response.Body()))
	require.Contains(t, string(export.Response.Header.Peek("Content-Type")), "text/csv")
	require.Contains(t, string(export.Response.Body()), own.ID.String())
	require.NotContains(t, string(export.Response.Body()), foreign.ID.String())
	var permission models.Permission
	require.NoError(t, app.DB.First(&permission, "resource=? AND action=?", models.ResourceAuditLogs, models.ActionExport).Error)
	require.NoError(t, app.DB.Where("custom_role_id=? AND permission_id=?", role.ID, permission.ID).Delete(&models.RolePermission{}).Error)
	denied := call("POST", "/api/audit-logs/export?resource_type=contact")
	require.Equal(t, 403, denied.Response.StatusCode(), "a cached grant cannot export after revocation")
	require.NotContains(t, string(denied.Response.Header.Peek("Content-Type")), "text/csv")
	var count int64
	require.NoError(t, app.DB.Model(&models.AuditLog{}).Where("organization_id=? AND event='audit.exported'", org.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
