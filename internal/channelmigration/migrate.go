// Package channelmigration contains additive compatibility migrations shared
// by production startup and disposable database fixtures.
package channelmigration

import "gorm.io/gorm"

// Run preserves every existing WhatsApp template reference and its foreign key.
// SMS campaigns have no WhatsApp template, so the legacy column must permit NULL.
func Run(db *gorm.DB) error {
	if !db.Migrator().HasTable("bulk_message_campaigns") {
		return nil
	}
	var required bool
	if err := db.Raw("SELECT attnotnull FROM pg_attribute WHERE attrelid = 'bulk_message_campaigns'::regclass AND attname = 'template_id'").Scan(&required).Error; err != nil {
		return err
	}
	if required {
		return db.Exec("ALTER TABLE bulk_message_campaigns ALTER COLUMN template_id DROP NOT NULL").Error
	}
	return nil
}
