// Package plan enforces optional operator allowances across one isolated database.
package plan

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/config"
	"gorm.io/gorm"
)

func Classify(err error) (string, bool) {
	if errors.Is(err, ErrStorageLimit) {
		return "storage", true
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" {
		return "", false
	}
	switch pg.ConstraintName {
	case "whatomate_plan_users_limit":
		return "users", true
	case "whatomate_plan_numbers_limit":
		return "numbers", true
	case "whatomate_plan_organizations_limit":
		return "organizations", true
	}
	return "", false
}

// Install adopts existing data; reducing an allowance never deletes records.
// A marked database cannot silently reopen with disabled operator controls.
func Install(db *gorm.DB, cfg config.PlanConfig) error {
	if db == nil || db.Name() != "postgres" {
		return errors.New("allowances require PostgreSQL")
	}
	if !cfg.Enabled {
		var exists bool
		if err := db.Raw("SELECT to_regclass('whatomate_allowance_state') IS NOT NULL").Scan(&exists).Error; err != nil {
			return err
		}
		if exists {
			return errors.New("allowance database requires enabled operator configuration")
		}
		return nil
	}
	if cfg.MaxUsers < 1 || cfg.MaxNumbers < 1 || cfg.MaxStorageBytes < 1 || cfg.MaxOrganizations < 0 {
		return errors.New("invalid allowances")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", storageLockID).Error; err != nil {
			return err
		}
		if err := tx.Exec("LOCK TABLE organizations,users,whatsapp_accounts IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS whatomate_allowance_state(singleton bool PRIMARY KEY DEFAULT true CHECK(singleton), max_users bigint NOT NULL, max_numbers bigint NOT NULL, max_storage_bytes bigint NOT NULL, max_organizations bigint NOT NULL, used_users bigint NOT NULL CHECK(used_users>=0), used_numbers bigint NOT NULL CHECK(used_numbers>=0), used_organizations bigint NOT NULL CHECK(used_organizations>=0))`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO whatomate_allowance_state SELECT true,?,?,?,?,(SELECT count(*) FROM users WHERE deleted_at IS NULL),(SELECT count(*) FROM whatsapp_accounts WHERE deleted_at IS NULL),(SELECT count(*) FROM organizations WHERE deleted_at IS NULL) ON CONFLICT(singleton) DO UPDATE SET max_users=excluded.max_users,max_numbers=excluded.max_numbers,max_storage_bytes=excluded.max_storage_bytes,max_organizations=excluded.max_organizations,used_users=excluded.used_users,used_numbers=excluded.used_numbers,used_organizations=excluded.used_organizations`, cfg.MaxUsers, cfg.MaxNumbers, cfg.MaxStorageBytes, cfg.MaxOrganizations).Error; err != nil {
			return err
		}
		return tx.Exec(databaseGuards).Error
	})
}
func Usage(db *gorm.DB, _ config.PlanConfig) (users, numbers int64, err error) {
	if err = db.Table("users").Where("deleted_at IS NULL").Count(&users).Error; err != nil {
		return
	}
	err = db.Table("whatsapp_accounts").Where("deleted_at IS NULL").Count(&numbers).Error
	return
}

const databaseGuards = `
CREATE OR REPLACE FUNCTION whatomate_allowance_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_counted boolean:=false;new_counted boolean:=false;delta integer;
BEGIN
 IF TG_OP<>'INSERT' THEN old_counted:=OLD.deleted_at IS NULL;END IF;
 IF TG_OP<>'DELETE' THEN new_counted:=NEW.deleted_at IS NULL;END IF;
 delta:=new_counted::integer-old_counted::integer;
 IF delta=0 THEN RETURN NULL;END IF;
 IF TG_TABLE_NAME='users' THEN
  UPDATE whatomate_allowance_state SET used_users=used_users+delta WHERE singleton=true AND (delta<0 OR used_users<max_users);
  IF NOT FOUND THEN RAISE EXCEPTION 'User allowance reached' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_users_limit';END IF;
 ELSIF TG_TABLE_NAME='whatsapp_accounts' THEN
  UPDATE whatomate_allowance_state SET used_numbers=used_numbers+delta WHERE singleton=true AND (delta<0 OR used_numbers<max_numbers);
  IF NOT FOUND THEN RAISE EXCEPTION 'Number allowance reached' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_numbers_limit';END IF;
 ELSE
  UPDATE whatomate_allowance_state SET used_organizations=used_organizations+delta WHERE singleton=true AND (delta<0 OR max_organizations=0 OR used_organizations<max_organizations);
  IF NOT FOUND THEN RAISE EXCEPTION 'Organization allowance reached' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_organizations_limit';END IF;
 END IF;
 RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS whatomate_allowance_users ON users;
CREATE TRIGGER whatomate_allowance_users AFTER INSERT OR UPDATE OR DELETE ON users FOR EACH ROW EXECUTE FUNCTION whatomate_allowance_guard();
DROP TRIGGER IF EXISTS whatomate_allowance_numbers ON whatsapp_accounts;
CREATE TRIGGER whatomate_allowance_numbers AFTER INSERT OR UPDATE OR DELETE ON whatsapp_accounts FOR EACH ROW EXECUTE FUNCTION whatomate_allowance_guard();
DROP TRIGGER IF EXISTS whatomate_allowance_organizations ON organizations;
CREATE TRIGGER whatomate_allowance_organizations AFTER INSERT OR UPDATE OR DELETE ON organizations FOR EACH ROW EXECUTE FUNCTION whatomate_allowance_guard();
`
