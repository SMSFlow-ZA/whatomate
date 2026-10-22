package models

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GenericOrganizationAllowed covers background dispatch as well as HTTP. It
// intentionally includes deleted rows and denies on storage uncertainty.
func GenericOrganizationAllowed(ctx context.Context, db *gorm.DB, org uuid.UUID) bool {
	if db == nil || org == uuid.Nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var allowed bool
	err := db.WithContext(ctx).Raw(`SELECT EXISTS(
 SELECT 1 FROM whatomate_protection_state WHERE id=1 AND protected=false)
 AND NOT EXISTS(
 SELECT 1 FROM contacts WHERE organization_id=? AND
 (protection_organization_id<>'' OR protection_module<>'' OR protection_resource<>'' OR protection_classification<>'')
 UNION ALL SELECT 1 FROM messages WHERE organization_id=? AND
 (protection_organization_id<>'' OR protection_module<>'' OR protection_resource<>'' OR protection_classification<>''))`, org, org).Scan(&allowed).Error
	return err == nil && allowed
}

// InstallProtectedHostGuard explicitly initializes ordinary-host compatibility.
// Runtime generic access denies if this migration or its singleton is absent.
func InstallProtectedHostGuard(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "postgres" {
		return fmt.Errorf("protected host state requires PostgreSQL")
	}
	return db.Transaction(installProtectedHostGuard)
}

func installProtectedHostGuard(tx *gorm.DB) error {
	for _, sql := range []string{
		`CREATE TABLE IF NOT EXISTS whatomate_protection_state (id smallint PRIMARY KEY CHECK (id=1), protected boolean NOT NULL DEFAULT false)`,
		`INSERT INTO whatomate_protection_state (id,protected) VALUES (1,false) ON CONFLICT (id) DO NOTHING`,
		`CREATE OR REPLACE FUNCTION whatomate_protection_state_monotonic() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' OR TG_OP = 'TRUNCATE' THEN
  RAISE EXCEPTION 'immutable protected host state';
 END IF;
 IF OLD.id IS DISTINCT FROM NEW.id OR (OLD.protected AND NOT NEW.protected) THEN
  RAISE EXCEPTION 'immutable protected host state';
 END IF;
 RETURN NEW;
END $$`,
		`DROP TRIGGER IF EXISTS whatomate_protection_state_row ON whatomate_protection_state`,
		`CREATE TRIGGER whatomate_protection_state_row BEFORE UPDATE OR DELETE ON whatomate_protection_state FOR EACH ROW EXECUTE FUNCTION whatomate_protection_state_monotonic()`,
		`DROP TRIGGER IF EXISTS whatomate_protection_state_truncate ON whatomate_protection_state`,
		`CREATE TRIGGER whatomate_protection_state_truncate BEFORE TRUNCATE ON whatomate_protection_state FOR EACH STATEMENT EXECUTE FUNCTION whatomate_protection_state_monotonic()`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}

// EnableProtectedHost is irreversible through application DML. Invoke only after
// production prerequisites pass, before ingress or workers can start. A restored
// database retains this boundary even when all private transcripts were erased.
func EnableProtectedHost(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("missing protected host database")
	}
	result := db.Exec(`UPDATE whatomate_protection_state SET protected=true WHERE id=1`)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("protected host state is uninitialized")
	}
	return nil
}

// ProtectedHostEnabled lets startup refuse a restored protected database when
// protected configuration is omitted. Storage uncertainty must refuse startup.
func ProtectedHostEnabled(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("missing protected host database")
	}
	var enabled bool
	result := db.Raw(`SELECT protected FROM whatomate_protection_state WHERE id=1`).Scan(&enabled)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, fmt.Errorf("protected host state is uninitialized")
	}
	return enabled, nil
}
