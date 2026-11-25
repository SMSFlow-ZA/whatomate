package database

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only creation of this new permission upgrades existing system Admin roles.
// Later explicit removals survive startup; custom roles/managers gain nothing.
func seedAuditExportPermission(db *gorm.DB, permission models.Permission) error {
	return db.Transaction(func(tx *gorm.DB) error {
		permission.ID = uuid.New()
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "resource"}, {Name: "action"}}, DoNothing: true}).Create(&permission)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return tx.Exec(`INSERT INTO role_permissions(custom_role_id,permission_id)
 SELECT r.id,? FROM custom_roles r WHERE r.is_system AND r.name='admin' AND r.deleted_at IS NULL
 AND EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id
 WHERE rp.custom_role_id=r.id AND p.resource=? AND p.action=? AND p.deleted_at IS NULL)
 ON CONFLICT DO NOTHING`, permission.ID, models.ResourceAuditLogs, models.ActionRead).Error
	})
}
