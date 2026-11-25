package database

import "gorm.io/gorm"

// InstallInboxFieldStamps is additive and idempotent. A database trigger covers
// all field writers, including a previous application image after rollback.
// Stamps are transactional: rolled-back edits never advance a saved revision.
func InstallInboxFieldStamps(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_stamp_inbox_field_value()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN
    EXECUTE format('INSERT INTO %I.inbox_field_stamps (organization_id,definition_id,subject_id,revision) VALUES ($1,$2,$3,1) ON CONFLICT (organization_id,definition_id,subject_id) DO UPDATE SET revision=inbox_field_stamps.revision+1', TG_TABLE_SCHEMA)
    USING OLD.organization_id, OLD.definition_id, OLD.subject_id;
  END IF;
  IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND (NEW.organization_id,NEW.definition_id,NEW.subject_id) IS DISTINCT FROM (OLD.organization_id,OLD.definition_id,OLD.subject_id)) THEN
    EXECUTE format('INSERT INTO %I.inbox_field_stamps (organization_id,definition_id,subject_id,revision) VALUES ($1,$2,$3,1) ON CONFLICT (organization_id,definition_id,subject_id) DO UPDATE SET revision=inbox_field_stamps.revision+1', TG_TABLE_SCHEMA)
    USING NEW.organization_id, NEW.definition_id, NEW.subject_id;
  END IF;
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END; $$`).Error; err != nil {
			return err
		}
		// Replace just our trigger, inside the same transaction. Existing values
		// do not need a fabricated edit: their initial snapshot is revision zero.
		if err := tx.Exec(`DROP TRIGGER IF EXISTS whatomate_inbox_field_stamp ON inbox_field_values`).Error; err != nil {
			return err
		}
		return tx.Exec(`CREATE TRIGGER whatomate_inbox_field_stamp AFTER INSERT OR UPDATE OR DELETE ON inbox_field_values FOR EACH ROW EXECUTE FUNCTION whatomate_stamp_inbox_field_value()`).Error
	})
}
