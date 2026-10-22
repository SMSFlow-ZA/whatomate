package plan

import (
	"errors"
	"github.com/shridarpatil/whatomate/internal/config"
	"gorm.io/gorm"
)

const externalSchemaSetting = "whatomate:verified-external-schema"

// WithExternalSchema is used only after the compiled schema verifier succeeds.
func WithExternalSchema(db *gorm.DB) *gorm.DB {
	// Set returns a mutable statement. Return a reusable cloning session so
	// Connection callbacks cannot replace this handle's pool with their pinned
	// connection and leave it closed afterward. Preserve the verified setting.
	return db.Set(externalSchemaSetting, true).Session(&gorm.Session{})
}
func InstallMediaSchema(db *gorm.DB) error {
	return db.Exec("CREATE TABLE IF NOT EXISTS whatomate_media_publications(id uuid PRIMARY KEY)").Error
}
func VerifyExternalPlan(db *gorm.DB, c config.PlanConfig) error {
	// Protected ER does not use the generic managed-plan writer. Ordinary managed
	// hosts retain their existing installer and behavior.
	if c.Enabled {
		return errors.New("external protected ER schema does not enable generic managed plans")
	}
	var exists bool
	if e := db.Raw("SELECT to_regclass('whatomate_plan_state') IS NOT NULL").Scan(&exists).Error; e != nil {
		return e
	}
	if exists {
		return errors.New("managed plan state requires its owner installer")
	}
	return nil
}
