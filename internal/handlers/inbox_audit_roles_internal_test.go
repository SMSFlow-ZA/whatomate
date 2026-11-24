package handlers

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

func auditRoleRequest(t *testing.T, f inboxAuditFixture, id uuid.UUID, req RoleRequest) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, req)
	testutil.SetAuthContext(r, f.org.ID, f.user.ID)
	r.RequestCtx.SetUserValue("is_super_admin", true)
	if id != uuid.Nil {
		testutil.SetPathParam(r, "id", id.String())
	}
	return r
}

func failOwnedSecurityCapture(t *testing.T, f inboxAuditFixture) {
	t.Helper()
	name := "audit_owned_role_fault_" + uuid.NewString()
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if row, ok := db.Statement.Dest.(*models.AuditLog); ok && row.OrganizationID == f.org.ID && row.ContextVersion == 1 {
			_ = db.AddError(errors.New("synthetic audit storage failure"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, f.db.Callback().Create().Remove(name)) })
}

func TestInboxAuditRoleLifecycleRecordsAtomicVerifiedEvents(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	testutil.GetOrCreateTestPermissions(t, f.db)
	create := auditRoleRequest(t, f, uuid.Nil, RoleRequest{Name: "Synthetic audited role"})
	require.NoError(t, f.app.CreateRole(create))
	require.Equal(t, 200, create.RequestCtx.Response.StatusCode())
	var role models.CustomRole
	require.NoError(t, f.db.First(&role, "organization_id=? AND name=?", f.org.ID, "Synthetic audited role").Error)
	update := auditRoleRequest(t, f, role.ID, RoleRequest{Name: "Synthetic renamed role", Permissions: []string{"contacts:read"}})
	require.NoError(t, f.app.UpdateRole(update))
	require.Equal(t, 200, update.RequestCtx.Response.StatusCode())
	remove := auditRoleRequest(t, f, role.ID, RoleRequest{})
	require.NoError(t, f.app.DeleteRole(remove))
	require.Equal(t, 200, remove.RequestCtx.Response.StatusCode())
	rows := f.securityRows(t)
	require.Len(t, rows, 3)
	for i, reason := range []audit.Reason{audit.RoleCreated, audit.RoleChanged, audit.RoleDeleted} {
		require.Equal(t, string(reason), rows[i].Reason)
		require.Equal(t, role.ID, rows[i].ResourceID)
		require.Equal(t, "role", rows[i].ResourceType)
		require.Equal(t, string(audit.PermissionChanged), rows[i].Event)
		require.True(t, rows[i].ActorVerified)
		require.Equal(t, f.user.ID, rows[i].UserID)
		require.Empty(t, rows[i].Changes)
	}
	var retained models.CustomRole
	require.NoError(t, f.db.Unscoped().First(&retained, "id=?", role.ID).Error)
	require.True(t, retained.DeletedAt.Valid)
}

func TestInboxAuditRoleCreationFailureRestoresDefaultsAndLeavesNoRole(t *testing.T) {
	f := newInboxAuditAuthFixture(t)
	testutil.GetOrCreateTestPermissions(t, f.db)
	previous := models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Synthetic previous default", IsDefault: true}
	require.NoError(t, f.db.Create(&previous).Error)
	failOwnedSecurityCapture(t, f)
	r := auditRoleRequest(t, f, uuid.Nil, RoleRequest{Name: "Synthetic rejected role", IsDefault: true, Permissions: []string{"contacts:read"}})
	require.NoError(t, f.app.CreateRole(r))
	require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
	var retained models.CustomRole
	require.NoError(t, f.db.First(&retained, "id=?", previous.ID).Error)
	require.True(t, retained.IsDefault)
	var count int64
	require.NoError(t, f.db.Model(&models.CustomRole{}).Where("organization_id=? AND name=?", f.org.ID, "Synthetic rejected role").Count(&count).Error)
	require.Zero(t, count)
	require.Empty(t, f.securityRows(t))
}

func TestInboxAuditRoleEditAndDeleteFailuresRollbackPermissionsAndFields(t *testing.T) {
	for _, system := range []bool{false, true} {
		t.Run(map[bool]string{false: "custom", true: "system"}[system], func(t *testing.T) {
			f := newInboxAuditAuthFixture(t)
			testutil.GetOrCreateTestPermissions(t, f.db)
			var oldPermission, newPermission models.Permission
			require.NoError(t, f.db.First(&oldPermission, "resource=? AND action=?", models.ResourceChat, models.ActionRead).Error)
			require.NoError(t, f.db.First(&newPermission, "resource=? AND action=?", models.ResourceContacts, models.ActionRead).Error)
			role := models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Synthetic stable role", Description: "Original description", IsSystem: system, Permissions: []models.Permission{oldPermission}}
			require.NoError(t, f.db.Create(&role).Error)
			failOwnedSecurityCapture(t, f)
			r := auditRoleRequest(t, f, role.ID, RoleRequest{Name: "Rejected rename", Description: "Rejected description", IsDefault: true, Permissions: []string{newPermission.Resource + ":" + newPermission.Action}})
			require.NoError(t, f.app.UpdateRole(r))
			require.Equal(t, 500, r.RequestCtx.Response.StatusCode())
			var retained models.CustomRole
			require.NoError(t, f.db.Preload("Permissions").First(&retained, "id=?", role.ID).Error)
			require.Equal(t, "Synthetic stable role", retained.Name)
			require.Equal(t, "Original description", retained.Description)
			require.False(t, retained.IsDefault)
			require.Len(t, retained.Permissions, 1)
			require.Equal(t, oldPermission.ID, retained.Permissions[0].ID)
			if !system {
				remove := auditRoleRequest(t, f, role.ID, RoleRequest{})
				require.NoError(t, f.app.DeleteRole(remove))
				require.Equal(t, 500, remove.RequestCtx.Response.StatusCode())
				retained = models.CustomRole{}
				require.NoError(t, f.db.First(&retained, "id=?", role.ID).Error)
				require.False(t, retained.DeletedAt.Valid)
			}
			require.Empty(t, f.securityRows(t))
		})
	}
}
