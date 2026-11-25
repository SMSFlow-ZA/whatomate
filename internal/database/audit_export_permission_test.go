package database

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuditExportPermissionUpgradeIsAtomicAndPreservesExplicitRemovals(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback"}[fault], func(t *testing.T) {
			db := testutil.SetupTestDB(t)
			rollback := errors.New("owned rehearsal rollback")
			err := db.Transaction(func(tx *gorm.DB) error {
				schema := "audit_seed_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				require.NoError(t, tx.Exec("CREATE SCHEMA "+schema).Error)
				for _, table := range []string{"permissions", "custom_roles", "role_permissions"} {
					require.NoError(t, tx.Exec("CREATE TABLE "+schema+"."+table+" (LIKE public."+table+" INCLUDING ALL)").Error)
				}
				require.NoError(t, tx.Exec("SET LOCAL search_path TO "+schema).Error)
				read := models.Permission{BaseModel: models.BaseModel{ID: uuid.New()}, Resource: models.ResourceAuditLogs, Action: models.ActionRead}
				require.NoError(t, tx.Create(&read).Error)
				roles := []models.CustomRole{
					{OrganizationID: uuid.New(), Name: "admin", IsSystem: true}, {OrganizationID: uuid.New(), Name: "manager", IsSystem: true}, {OrganizationID: uuid.New(), Name: "admin", IsSystem: false}, {OrganizationID: uuid.New(), Name: "admin", IsSystem: true},
				}
				for i := range roles {
					require.NoError(t, tx.Create(&roles[i]).Error)
					if i != 3 {
						require.NoError(t, tx.Create(&models.RolePermission{CustomRoleID: roles[i].ID, PermissionID: read.ID}).Error)
					}
				}
				name := "audit_export_seed_fault_" + uuid.NewString()
				if fault {
					require.NoError(t, tx.Callback().Raw().Before("gorm:raw").Register(name, func(current *gorm.DB) {
						if strings.Contains(current.Statement.SQL.String(), "INSERT INTO role_permissions") {
							_ = current.AddError(errors.New("synthetic grant failure"))
						}
					}))
					t.Cleanup(func() { require.NoError(t, db.Callback().Raw().Remove(name)) })
				}
				err := seedAuditExportPermission(tx, models.Permission{Resource: models.ResourceAuditLogs, Action: models.ActionExport})
				var export models.Permission
				if fault {
					require.Error(t, err)
					var count int64
					require.NoError(t, tx.Model(&models.Permission{}).Where("resource=? AND action=?", models.ResourceAuditLogs, models.ActionExport).Count(&count).Error)
					require.Zero(t, count)
					require.NoError(t, tx.Callback().Raw().Remove(name))
					require.NoError(t, SeedPermissionsAndRoles(tx))
				} else {
					require.NoError(t, err)
				}
				require.NoError(t, tx.First(&export, "resource=? AND action=?", models.ResourceAuditLogs, models.ActionExport).Error)
				for i, role := range roles {
					var count int64
					require.NoError(t, tx.Model(&models.RolePermission{}).Where("custom_role_id=? AND permission_id=?", role.ID, export.ID).Count(&count).Error)
					require.Equal(t, i == 0, count == 1)
				}
				require.NoError(t, tx.Where("custom_role_id=? AND permission_id=?", roles[0].ID, export.ID).Delete(&models.RolePermission{}).Error)
				require.NoError(t, SeedPermissionsAndRoles(tx))
				var count int64
				require.NoError(t, tx.Model(&models.RolePermission{}).Where("custom_role_id=? AND permission_id=?", roles[0].ID, export.ID).Count(&count).Error)
				require.Zero(t, count)
				return rollback
			})
			require.ErrorIs(t, err, rollback)
		})
	}
}
