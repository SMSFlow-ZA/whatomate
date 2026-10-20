package models

import (
	"fmt"
	"github.com/google/uuid"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProtectionFields are separate from editable JSON metadata. Database triggers
// make the complete tuple immutable after insertion, including ordinary rows.
type ProtectionFields struct {
	ProtectionOrganizationID string `gorm:"type:text;not null;default:''" json:"-"`
	ProtectionModule         string `gorm:"type:text;not null;default:'';index" json:"-"`
	ProtectionResource       string `gorm:"type:text;not null;default:''" json:"-"`
	ProtectionClassification string `gorm:"type:text;not null;default:''" json:"-"`
}

func (p ProtectionFields) Protected() bool {
	return p.ProtectionOrganizationID != "" || p.ProtectionModule != "" || p.ProtectionResource != "" || p.ProtectionClassification != ""
}
func (p ProtectionFields) Value(org uuid.UUID) (ext.Protection, error) {
	if org == uuid.Nil || p.ProtectionOrganizationID != org.String() {
		return ext.Protection{}, ext.ErrDenied
	}
	return ext.NewProtection(p.ProtectionOrganizationID, p.ProtectionModule, p.ProtectionResource, p.ProtectionClassification)
}
func ProtectionFromValue(p ext.Protection) ProtectionFields {
	return ProtectionFields{p.OrganizationID(), p.ModuleID(), p.ResourceID(), p.Classification()}
}

// RegisterProtectionReadGuards hides protected transcripts from generic host
// queries. Raw SQL and derived resources also require the host organization gate.
// There is intentionally no admin/context bypass.
func RegisterProtectionReadGuards(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("missing protection database")
	}
	guard := func(tx *gorm.DB) {
		table := tx.Statement.Table
		if table != "contacts" && table != "messages" {
			return
		}
		for _, field := range []string{"protection_organization_id", "protection_module", "protection_resource", "protection_classification"} {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Table: table, Name: field}, Value: ""}}})
		}
		if table == "messages" {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: `NOT EXISTS (SELECT 1 FROM contacts protection_parent WHERE protection_parent.id = messages.contact_id AND (protection_parent.protection_organization_id <> '' OR protection_parent.protection_module <> '' OR protection_parent.protection_resource <> '' OR protection_parent.protection_classification <> ''))`}}})
		} else {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: `NOT EXISTS (SELECT 1 FROM messages protection_child WHERE protection_child.contact_id = contacts.id AND (protection_child.protection_organization_id <> '' OR protection_child.protection_module <> '' OR protection_child.protection_resource <> '' OR protection_child.protection_classification <> ''))`}}})
		}
	}
	if err := db.Callback().Query().Before("gorm:query").Register("extensions:protected_read", guard); err != nil {
		return err
	}
	return db.Callback().Row().Before("gorm:row").Register("extensions:protected_row", guard)
}

// InstallProtectionGuards runs after AutoMigrate and before workers. It covers
// SQL/map updates that bypass model hooks as well as malformed tuples.
func InstallProtectionGuards(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "postgres" {
		return fmt.Errorf("protection requires PostgreSQL")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE OR REPLACE FUNCTION whatomate_protection_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' AND ROW(OLD.organization_id, OLD.protection_organization_id, OLD.protection_module, OLD.protection_resource, OLD.protection_classification) IS DISTINCT FROM ROW(NEW.organization_id, NEW.protection_organization_id, NEW.protection_module, NEW.protection_resource, NEW.protection_classification) THEN
  RAISE EXCEPTION 'immutable protection';
 END IF;
 IF NOT ((NEW.protection_organization_id = '' AND NEW.protection_module = '' AND NEW.protection_resource = '' AND NEW.protection_classification = '') OR
 (NEW.protection_organization_id = NEW.organization_id::text AND NEW.protection_module ~ '^[a-z][a-z0-9-]{0,62}$' AND NEW.protection_resource <> '' AND NEW.protection_classification <> '')) THEN
  RAISE EXCEPTION 'invalid protection';
 END IF;
 RETURN NEW;
END $$`).Error; err != nil {
			return err
		}
		for _, table := range []string{"contacts", "messages"} {
			if err := tx.Exec("DROP TRIGGER IF EXISTS whatomate_protection_immutable ON " + table).Error; err != nil {
				return err
			}
			if err := tx.Exec("CREATE TRIGGER whatomate_protection_immutable BEFORE INSERT OR UPDATE ON " + table + " FOR EACH ROW EXECUTE FUNCTION whatomate_protection_immutable()").Error; err != nil {
				return err
			}
		}
		return nil
	})
}
