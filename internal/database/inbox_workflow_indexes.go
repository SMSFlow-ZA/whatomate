package database

import "gorm.io/gorm"

var inboxWorkflowIndexSQL = []string{
	`CREATE INDEX IF NOT EXISTS idx_inbox_workflow_pending_event ON inbox_case_activities (created_at, id) WHERE deleted_at IS NULL AND workflow_processed_at IS NULL AND workflow_trigger <> ''`,
	`CREATE INDEX IF NOT EXISTS idx_inbox_workflow_pending_case ON inbox_case_activities (organization_id, case_id, created_at, id) WHERE deleted_at IS NULL AND workflow_processed_at IS NULL AND workflow_trigger <> ''`,
	`CREATE INDEX IF NOT EXISTS idx_inbox_workflow_pending_send ON inbox_workflow_runs (created_at, id) WHERE deleted_at IS NULL AND state = 'pending'`,
}

// CreateInboxWorkflowIndexes also lets focused database tests exercise the
// actual migration definitions without changing unrelated fixture constraints.
func CreateInboxWorkflowIndexes(db *gorm.DB) error {
	for _, statement := range inboxWorkflowIndexSQL {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
