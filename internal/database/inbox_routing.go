package database

import "gorm.io/gorm"

// InstallInboxAssignmentReceipts is additive and idempotent. Existing ownership
// is not fabricated as history. A previous application image can still write
// cases after rollback, and those real changes retain their transactional receipt.
func InstallInboxAssignmentReceipts(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_record_inbox_assignment()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.assignee_id IS NULL THEN RETURN NEW; END IF;
  IF TG_OP = 'UPDATE' THEN
    IF (NEW.organization_id,NEW.account_id,NEW.assignee_id) IS NOT DISTINCT FROM
       (OLD.organization_id,OLD.account_id,OLD.assignee_id) THEN RETURN NEW; END IF;
  END IF;
  EXECUTE format('INSERT INTO %I.inbox_assignment_receipts (organization_id,account_id,agent_id,case_id,assigned_at) VALUES ($1,$2,$3,$4,statement_timestamp())', TG_TABLE_SCHEMA)
  USING NEW.organization_id, NEW.account_id, NEW.assignee_id, NEW.id;
  RETURN NEW;
END; $$`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DROP TRIGGER IF EXISTS whatomate_inbox_assignment_receipt ON inbox_cases`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE TRIGGER whatomate_inbox_assignment_receipt AFTER INSERT OR UPDATE OF assignee_id,organization_id,account_id ON inbox_cases FOR EACH ROW EXECUTE FUNCTION whatomate_record_inbox_assignment()`).Error; err != nil {
			return err
		}
		return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_inbox_routing_priority ON inbox_cases
 (organization_id,account_id,team_id,(CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END),opened_at,id)
 WHERE deleted_at IS NULL AND assignee_id IS NULL AND status IN ('open','pending')`).Error
	})
}
