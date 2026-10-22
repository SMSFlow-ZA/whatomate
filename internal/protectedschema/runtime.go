package protectedschema

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
)

const sealDDL = `CREATE TABLE IF NOT EXISTS whatomate_schema_attempt(release text NOT NULL,catalog_sha256 text NOT NULL,PRIMARY KEY(release,catalog_sha256));
CREATE TABLE IF NOT EXISTS whatomate_schema_seal(generation bigint GENERATED ALWAYS AS IDENTITY UNIQUE,release text NOT NULL,catalog_sha256 text NOT NULL,PRIMARY KEY(release,catalog_sha256));
CREATE OR REPLACE FUNCTION whatomate_schema_seal_immutable() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'immutable schema release evidence';END$$;
DROP TRIGGER IF EXISTS whatomate_schema_seal_row ON whatomate_schema_seal;
CREATE TRIGGER whatomate_schema_seal_row BEFORE UPDATE OR DELETE ON whatomate_schema_seal FOR EACH ROW EXECUTE FUNCTION whatomate_schema_seal_immutable();
DROP TRIGGER IF EXISTS whatomate_schema_seal_truncate ON whatomate_schema_seal;
CREATE TRIGGER whatomate_schema_seal_truncate BEFORE TRUNCATE ON whatomate_schema_seal FOR EACH STATEMENT EXECUTE FUNCTION whatomate_schema_seal_immutable();`

type Lease struct{ conn *sql.Conn }

func (l *Lease) Close() error {
	if l == nil || l.conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, e := l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock_shared($1)", LockID)
	return errors.Join(e, l.conn.Close())
}
func (l *Lease) Check(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return ErrDenied
	}
	var held bool
	e := l.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ShareLock' AND classid=($1::bigint >> 32)::oid AND objid=($1::bigint & 4294967295)::oid AND objsubid=1)`, LockID).Scan(&held)
	if e != nil || !held {
		return ErrDenied
	}
	return nil
}

// Acquire holds the same shared physical-session migration lease until Close.
// The offline installer refuses its exclusive lease before performing any DDL.
func Acquire(ctx context.Context, db *sql.DB, c *config.Config) (*Lease, error) {
	if Validate(c) != nil || db == nil || !guarded(db) {
		return nil, ErrDenied
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		return nil, e
	}
	lease := &Lease{conn: conn}
	var held bool
	e = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock_shared($1)", LockID).Scan(&held)
	if e != nil || !held {
		conn.Close()
		return nil, ErrDenied
	}
	if e = verify(ctx, conn, c); e != nil {
		lease.Close()
		return nil, e
	}
	return lease, nil
}
func Verify(ctx context.Context, db *sql.DB, c *config.Config) error {
	lease, e := Acquire(ctx, db, c)
	if e != nil {
		return e
	}
	return lease.Close()
}

func verify(ctx context.Context, conn *sql.Conn, c *config.Config) error {
	tx, e := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelSerializable})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var namespace string
	var forbidden bool
	var identity bool
	if e = tx.QueryRowContext(ctx, "SELECT session_user=current_user AND current_user=$1 AND current_database()=$2", c.Database.User, c.Database.Name).Scan(&identity); e != nil || !identity {
		return ErrDenied
	}
	if e = tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&namespace); e != nil || namespace != "public" {
		return ErrDenied
	}
	// No role memberships, DDL capabilities, schema/database ownership or table /
	// routine ownership. PUBLIC TEMP/CREATE must also have been revoked offline.
	e = tx.QueryRowContext(ctx, `SELECT r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls
 OR EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid)
 OR EXISTS(SELECT 1 FROM pg_database WHERE datname=current_database() AND datdba=r.oid)
 OR has_database_privilege(current_user,current_database(),'CREATE,TEMP')
 OR EXISTS(SELECT 1 FROM pg_namespace n WHERE left(n.nspname,3)<>'pg_' AND n.nspname NOT IN('public','information_schema'))
 OR EXISTS(SELECT 1 FROM pg_namespace n WHERE left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema' AND (n.nspowner=r.oid OR has_schema_privilege(current_user,n.oid,'CREATE')))
 OR EXISTS(SELECT 1 FROM pg_class v JOIN pg_namespace n ON n.oid=v.relnamespace WHERE left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema' AND (v.relowner=r.oid OR (v.relkind IN('r','p') AND (has_table_privilege(current_user,v.oid,'TRIGGER,REFERENCES,TRUNCATE') OR has_any_column_privilege(current_user,v.oid,'REFERENCES')))))
 OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema' AND p.proowner=r.oid)
 OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='pg_catalog' AND p.proname IN('pg_read_file','pg_read_binary_file','pg_ls_dir','pg_stat_file','pg_write_file','pg_file_write','pg_file_rename','pg_file_unlink','lo_import','lo_export') AND has_function_privilege(current_user,p.oid,'EXECUTE'))
 FROM pg_roles r WHERE r.rolname=current_user`).Scan(&forbidden)
	if e != nil || forbidden {
		return ErrDenied
	}
	var release, shape string
	if e = tx.QueryRowContext(ctx, "SELECT release,catalog_sha256 FROM whatomate_schema_seal ORDER BY generation DESC LIMIT 1").Scan(&release, &shape); e != nil || release != ReleaseIdentity || shape != ExpectedCatalogSHA256 {
		return ErrDenied
	}
	var pending bool
	if e = tx.QueryRowContext(ctx, pendingSQL).Scan(&pending); e != nil || pending {
		return ErrDenied
	}
	var protected bool
	if e = tx.QueryRowContext(ctx, "SELECT protected FROM whatomate_protection_state WHERE id=1").Scan(&protected); e != nil || !protected {
		return ErrDenied
	}
	for _, table := range []string{"whatomate_schema_seal", "whatomate_schema_attempt", "extensions_bootstrap_attempt", "whatomate_protection_state", "extension_security_schema", "extension_schema", "addon_cases_schema", "addon_knowledge_vector_profiles", "addon_knowledge_vector_workers", "er_processing_authorization_bindings"} {
		var exists bool
		if e = tx.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			continue
		}
		e = tx.QueryRowContext(ctx, "SELECT has_table_privilege(current_user,$1,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER,REFERENCES') OR has_any_column_privilege(current_user,$1,'INSERT,UPDATE,REFERENCES')", table).Scan(&forbidden)
		if e != nil || forbidden {
			return ErrDenied
		}
	}
	if _, e = tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog"); e != nil {
		return e
	}
	actual, e := capture(ctx, tx, namespace)
	if e != nil || actual.SHA256 != ExpectedCatalogSHA256 {
		return ErrDenied
	}
	return tx.Commit()
}

// Once an owner attempt is durable, loss of its advisory session cannot allow a
// runtime or another installer through while pool-owned migrations wind down.
// Baseline evidence exists only in a fresh reference DB and is never a seal.
const pendingSQL = `SELECT EXISTS(SELECT 1 FROM whatomate_schema_attempt a WHERE a.catalog_sha256<>'baseline' AND NOT EXISTS(SELECT 1 FROM whatomate_schema_seal s WHERE s.release=a.release AND s.catalog_sha256=a.catalog_sha256))`

func exclusiveHeld(ctx context.Context, conn *sql.Conn) error {
	var held bool
	e := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ExclusiveLock' AND classid=($1::bigint >> 32)::oid AND objid=($1::bigint & 4294967295)::oid AND objsubid=1)`, LockID).Scan(&held)
	if e != nil || !held {
		return ErrDenied
	}
	return nil
}
