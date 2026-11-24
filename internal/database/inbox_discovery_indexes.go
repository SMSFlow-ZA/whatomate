package database

import "gorm.io/gorm"

var inboxDiscoveryIndexSQL = []string{
	`CREATE INDEX IF NOT EXISTS idx_inbox_discovery_opened ON inbox_cases (organization_id, opened_at DESC, id DESC) WHERE deleted_at IS NULL`,
	`CREATE INDEX IF NOT EXISTS idx_inbox_discovery_activity ON inbox_cases (organization_id, (GREATEST(opened_at,last_inbound_at,last_human_reply_at)) DESC, id DESC) WHERE deleted_at IS NULL`,
}

// These additive indexes support discovery without a mutable activity column.
// Older binaries' writes to the underlying timestamps keep them correct too.
func CreateInboxDiscoveryIndexes(db *gorm.DB) error {
	for _, statement := range inboxDiscoveryIndexSQL {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
