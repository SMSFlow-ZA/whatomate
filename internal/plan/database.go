// Package plan enforces host-configured allowances across an isolated database.
package plan

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/config"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Classify recognizes structured, stable quota errors, including wrapped errors.
func Classify(err error) (resource string, ok bool) {
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
	}
	return "", false
}

func IsSupportProtected(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514" && pg.ConstraintName == "whatomate_plan_support_protected"
}

// EnsureSupport provisions the reserved account once, before installing guards.
// Existing accounts must already be active superadmins; their password is never reset.
func EnsureSupport(db *gorm.DB, cfg config.PlanConfig, ownerEmail string) error {
	if !cfg.Enabled {
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(cfg.SupportEmail))
	if email == "" || email == strings.ToLower(strings.TrimSpace(ownerEmail)) {
		return errors.New("managed support and owner identities must be distinct")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(1464814930)").Error; err != nil {
			return err
		}
		var support []struct {
			ID           uuid.UUID
			IsActive     bool
			IsSuperAdmin bool
			DeletedAt    *string
		}
		if err := tx.Raw("SELECT id,is_active,is_super_admin,deleted_at::text FROM users WHERE lower(btrim(email))=?", email).Scan(&support).Error; err != nil {
			return err
		}
		if len(support) > 0 {
			if len(support) != 1 || !support[0].IsActive || !support[0].IsSuperAdmin || support[0].DeletedAt != nil {
				return errors.New("configured support account must be a unique active superadmin")
			}
			return nil
		}
		if len(cfg.SupportPassword) < 12 {
			return errors.New("initial managed support password must contain at least 12 characters")
		}
		var owner []struct{ OrganizationID uuid.UUID }
		if err := tx.Raw("SELECT organization_id FROM users WHERE lower(btrim(email))=? AND deleted_at IS NULL", strings.ToLower(strings.TrimSpace(ownerEmail))).Scan(&owner).Error; err != nil {
			return err
		}
		if len(owner) != 1 || owner[0].OrganizationID == uuid.Nil {
			return errors.New("managed support bootstrap requires an existing owner organization")
		}
		var roles []struct{ ID uuid.UUID }
		if err := tx.Raw("SELECT id FROM custom_roles WHERE organization_id=? AND name='admin' AND is_system=true AND deleted_at IS NULL", owner[0].OrganizationID).Scan(&roles).Error; err != nil {
			return err
		}
		if len(roles) != 1 {
			return errors.New("managed support bootstrap requires the system admin role")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(cfg.SupportPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		id := uuid.New()
		if err := tx.Exec("INSERT INTO users(id,email,organization_id,password_hash,full_name,role_id,is_active,is_super_admin) VALUES (?,?,?,?,?,?,true,true)", id, email, owner[0].OrganizationID, string(hash), "Support", roles[0].ID).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO user_organizations(id,user_id,organization_id,role_id,is_default) VALUES (?,?,?,?,true)", uuid.New(), id, owner[0].OrganizationID, roles[0].ID).Error
	})
}

// Install adopts existing usage and atomically installs PostgreSQL guards. Quota
// counters avoid stale-snapshot count races at stronger transaction isolation.
// Lower limits preserve all existing records and their ordinary updates.
func Install(db *gorm.DB, cfg config.PlanConfig) error {
	if db == nil || db.Name() != "postgres" {
		return errors.New("plan guards require PostgreSQL")
	}
	if !cfg.Enabled {
		var exists bool
		if err := db.Raw("SELECT to_regclass('whatomate_plan_state') IS NOT NULL").Scan(&exists).Error; err != nil {
			return err
		}
		if exists {
			return errors.New("managed database requires enabled host plan configuration")
		}
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(cfg.SupportEmail))
	if cfg.MaxUsers <= 0 || cfg.MaxNumbers <= 0 || cfg.MaxStorageBytes <= 0 || email == "" {
		return errors.New("managed plan requires positive allowances and support identity")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		// Writers/status readers acquire storage before any entity table locks.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", storageLockID).Error; err != nil {
			return err
		}
		if err := tx.Exec("LOCK TABLE users,whatsapp_accounts,user_organizations IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(1464814930)").Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS whatomate_plan_state (
   singleton bool PRIMARY KEY DEFAULT true CHECK(singleton), max_users bigint NOT NULL CHECK(max_users>0), max_numbers bigint NOT NULL CHECK(max_numbers>0), max_storage_bytes bigint NOT NULL CHECK(max_storage_bytes>0),
   used_users bigint NOT NULL CHECK(used_users>=0), used_numbers bigint NOT NULL CHECK(used_numbers>=0), support_email text NOT NULL, support_user_id uuid NOT NULL
  )`).Error; err != nil {
			return err
		}
		if err := tx.Exec("ALTER TABLE whatomate_plan_state ADD COLUMN IF NOT EXISTS max_storage_bytes bigint CHECK(max_storage_bytes>0)").Error; err != nil {
			return err
		}
		var support []struct {
			ID           uuid.UUID
			IsActive     bool
			IsSuperAdmin bool
			DeletedAt    *string
		}
		if err := tx.Raw("SELECT id,is_active,is_super_admin,deleted_at::text FROM users WHERE lower(btrim(email))=?", email).Scan(&support).Error; err != nil {
			return err
		}
		if len(support) != 1 || !support[0].IsActive || !support[0].IsSuperAdmin || support[0].DeletedAt != nil {
			return errors.New("configured support account must exist as a unique active superadmin before installing plan guards")
		}
		var previous []struct {
			SupportEmail  string
			SupportUserID uuid.UUID
		}
		if err := tx.Raw("SELECT support_email,support_user_id FROM whatomate_plan_state").Scan(&previous).Error; err != nil {
			return err
		}
		if len(previous) > 0 && (previous[0].SupportEmail != email || previous[0].SupportUserID != support[0].ID) {
			return errors.New("managed support identity cannot be reassigned")
		}
		if err := tx.Exec(`INSERT INTO whatomate_plan_state(singleton,max_users,max_numbers,max_storage_bytes,used_users,used_numbers,support_email,support_user_id)
   SELECT true,?,?,?,(SELECT count(*) FROM users WHERE deleted_at IS NULL AND id<>?),(SELECT count(*) FROM whatsapp_accounts WHERE deleted_at IS NULL),?,?
   ON CONFLICT(singleton) DO UPDATE SET max_users=excluded.max_users,max_numbers=excluded.max_numbers,max_storage_bytes=excluded.max_storage_bytes,used_users=excluded.used_users,used_numbers=excluded.used_numbers`, cfg.MaxUsers, cfg.MaxNumbers, cfg.MaxStorageBytes, support[0].ID, email, support[0].ID).Error; err != nil {
			return err
		}
		if err := tx.Exec("ALTER TABLE whatomate_plan_state ALTER COLUMN max_storage_bytes SET NOT NULL").Error; err != nil {
			return err
		}
		return tx.Exec(databaseGuards).Error
	})
}

// Usage counts nondeleted records globally, including disabled/pending ones.
func Usage(db *gorm.DB, cfg config.PlanConfig) (users int64, numbers int64, err error) {
	query := db.Table("users").Where("deleted_at IS NULL")
	if cfg.Enabled {
		var managed bool
		if err = db.Raw("SELECT to_regclass('whatomate_plan_state') IS NOT NULL").Scan(&managed).Error; err != nil {
			return
		}
		if managed {
			query = query.Where("id <> (SELECT support_user_id FROM whatomate_plan_state WHERE singleton=true)")
		} else {
			query = query.Where("lower(btrim(email)) <> ?", strings.ToLower(strings.TrimSpace(cfg.SupportEmail)))
		}
	}
	if err = query.Count(&users).Error; err != nil {
		return
	}
	err = db.Table("whatsapp_accounts").Where("deleted_at IS NULL").Count(&numbers).Error
	return
}

const databaseGuards = `
CREATE OR REPLACE FUNCTION whatomate_plan_entity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE state whatomate_plan_state%ROWTYPE; old_counted boolean := false; new_counted boolean := false; delta integer;
BEGIN
 PERFORM pg_advisory_xact_lock(1464814930);
 SELECT * INTO STRICT state FROM whatomate_plan_state WHERE singleton=true;
 IF TG_TABLE_NAME='users' THEN
  IF TG_OP<>'INSERT' AND OLD.id=state.support_user_id THEN
   IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Support account is protected' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_support_protected'; END IF;
   IF ROW(NEW.id,NEW.email,NEW.organization_id,NEW.full_name,NEW.password_hash,NEW.role_id,NEW.is_active,NEW.is_super_admin,NEW.sso_provider,NEW.sso_provider_id,NEW.deleted_at)
    IS DISTINCT FROM ROW(OLD.id,OLD.email,OLD.organization_id,OLD.full_name,OLD.password_hash,OLD.role_id,OLD.is_active,OLD.is_super_admin,OLD.sso_provider,OLD.sso_provider_id,OLD.deleted_at) THEN
    RAISE EXCEPTION 'Support account is protected' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_support_protected';
   END IF;
  END IF;
  IF TG_OP<>'DELETE' AND NEW.id<>state.support_user_id AND lower(btrim(NEW.email))=state.support_email THEN
   RAISE EXCEPTION 'Support account is protected' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_support_protected';
  END IF;
  IF TG_OP<>'INSERT' THEN old_counted:=OLD.deleted_at IS NULL AND OLD.id<>state.support_user_id; END IF;
  IF TG_OP<>'DELETE' THEN new_counted:=NEW.deleted_at IS NULL AND NEW.id<>state.support_user_id; END IF;
  delta:=new_counted::integer-old_counted::integer;
  IF delta<>0 THEN
   UPDATE whatomate_plan_state SET used_users=used_users+delta WHERE singleton=true AND (delta<0 OR used_users<max_users);
   IF NOT FOUND THEN RAISE EXCEPTION 'User limit reached' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_users_limit'; END IF;
  END IF;
 ELSE
  IF TG_OP<>'INSERT' THEN old_counted:=OLD.deleted_at IS NULL; END IF;
  IF TG_OP<>'DELETE' THEN new_counted:=NEW.deleted_at IS NULL; END IF;
  delta:=new_counted::integer-old_counted::integer;
  IF delta<>0 THEN
   UPDATE whatomate_plan_state SET used_numbers=used_numbers+delta WHERE singleton=true AND (delta<0 OR used_numbers<max_numbers);
   IF NOT FOUND THEN RAISE EXCEPTION 'WhatsApp number limit reached' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_numbers_limit'; END IF;
  END IF;
 END IF;
 RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS whatomate_plan_users_guard ON users;
CREATE TRIGGER whatomate_plan_users_guard AFTER INSERT OR UPDATE OR DELETE ON users FOR EACH ROW EXECUTE FUNCTION whatomate_plan_entity_guard();
DROP TRIGGER IF EXISTS whatomate_plan_numbers_guard ON whatsapp_accounts;
CREATE TRIGGER whatomate_plan_numbers_guard AFTER INSERT OR UPDATE OR DELETE ON whatsapp_accounts FOR EACH ROW EXECUTE FUNCTION whatomate_plan_entity_guard();
CREATE OR REPLACE FUNCTION whatomate_plan_membership_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE support_id uuid;
BEGIN
 PERFORM pg_advisory_xact_lock(1464814930);
 SELECT support_user_id INTO STRICT support_id FROM whatomate_plan_state WHERE singleton=true;
 IF (TG_OP<>'INSERT' AND OLD.user_id=support_id) OR (TG_OP<>'DELETE' AND NEW.user_id=support_id) THEN
  IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'Support account is protected' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_support_protected'; END IF;
  IF ROW(NEW.id,NEW.user_id,NEW.organization_id,NEW.role_id,NEW.is_default,NEW.deleted_at) IS DISTINCT FROM ROW(OLD.id,OLD.user_id,OLD.organization_id,OLD.role_id,OLD.is_default,OLD.deleted_at) THEN
   RAISE EXCEPTION 'Support account is protected' USING ERRCODE='23514',CONSTRAINT='whatomate_plan_support_protected';
  END IF;
 END IF;
 RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS whatomate_plan_membership_guard ON user_organizations;
CREATE TRIGGER whatomate_plan_membership_guard AFTER INSERT OR UPDATE OR DELETE ON user_organizations FOR EACH ROW EXECUTE FUNCTION whatomate_plan_membership_guard();
`
