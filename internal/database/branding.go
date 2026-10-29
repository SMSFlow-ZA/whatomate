package database

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Add only the new resource to existing system Admin roles; preserve customizations.
func SeedBrandingAdminPermissions(db *gorm.DB) error {
	var roles []models.CustomRole
	if err := db.Where("name = ? AND is_system = ?", "admin", true).Find(&roles).Error; err != nil {
		return err
	}
	var permissions []models.Permission
	if err := db.Where("resource = ?", models.ResourceSettingsBranding).Find(&permissions).Error; err != nil {
		return err
	}
	for _, role := range roles {
		if err := db.Model(&role).Association("Permissions").Append(permissions); err != nil {
			return err
		}
	}
	return nil
}
