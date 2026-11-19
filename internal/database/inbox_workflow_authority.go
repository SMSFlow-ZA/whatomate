package database

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Older definitions did not retain their editor. Require review before those
// rules run again; saving and enabling records the responsible current editor.
// Repeated migrations leave reviewed rules unchanged.
func MigrateInboxWorkflowAuthority(db *gorm.DB) error {
	return db.Model(&models.InboxWorkflow{}).
		Where("kind = 'rule' AND enabled_at IS NOT NULL AND updated_by_id IS NULL").
		Update("enabled_at", nil).Error
}
