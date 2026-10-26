package protectedschema

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/config"
)

func compatiblePG(t *testing.T, setup ...func(*sql.DB)) (*sql.DB, *config.Config, CompatibleRequest) {
	t.Helper()
	dsn := os.Getenv("EXTENSION_COMPATIBLE_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL admin EXTENSION_COMPATIBLE_TEST_DSN required")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Scheme == "" {
		t.Fatal("URL admin DSN required")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	stem := "compatible_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	role, fnowner := stem+"_owner", stem+"_fn"
	exec := func(db *sql.DB, statement string) {
		t.Helper()
		if _, e := db.Exec(statement); e != nil {
			t.Fatal(e)
		}
	}
	exec(admin, "CREATE ROLE "+role+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS")
	exec(admin, "CREATE ROLE "+fnowner+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS")
	exec(admin, "CREATE DATABASE "+stem+" OWNER "+role)
	ownerURL := *u
	ownerURL.Path = "/" + stem
	ownerURL.User = url.User(role)
	db, e := sql.Open("pgx", ownerURL.String())
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() {
		db.Close()
		admin.Exec("DROP DATABASE " + stem + " WITH (FORCE)")
		admin.Exec("DROP ROLE " + fnowner)
		admin.Exec("DROP ROLE " + role)
		admin.Close()
	})
	c, r := compatibleConfig(t)
	c.Database.Name = stem
	c.Database.User = role
	exec(db, `CREATE TABLE organizations(id uuid PRIMARY KEY,deleted_at timestamptz);
 CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid,email text,is_active boolean,deleted_at timestamptz);
 CREATE TABLE whatomate_protection_state(id smallint PRIMARY KEY,protected boolean NOT NULL);`+sealDDL)
	if _, e = db.Exec(`INSERT INTO organizations(id) VALUES($1);`, r.OrganizationID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO users VALUES($1,$2,$3,true,NULL)`, uuid.NewString(), r.OrganizationID, c.DefaultAdmin.Email); e != nil {
		t.Fatal(e)
	}
	exec(db, `INSERT INTO whatomate_protection_state VALUES(1,true)`)
	exec(db, `SELECT setval(pg_get_serial_sequence('whatomate_schema_seal','generation'),4)`)
	exec(db, `CREATE FUNCTION compatible_authorize() RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$SELECT true$$`)
	adminURL := *u
	adminURL.Path = "/" + stem
	inside, e := sql.Open("pgx", adminURL.String())
	if e != nil {
		t.Fatal(e)
	}
	exec(inside, "GRANT USAGE ON SCHEMA public TO "+fnowner)
	exec(inside, "ALTER FUNCTION compatible_authorize() OWNER TO "+fnowner)
	exec(inside, "REVOKE ALL ON FUNCTION compatible_authorize() FROM PUBLIC")
	exec(inside, "GRANT EXECUTE ON FUNCTION compatible_authorize() TO "+role)
	inside.Close()
	// A migration replay really cannot replace this accepted NOLOGIN-owned function.
	if _, e = db.Exec(`CREATE OR REPLACE FUNCTION compatible_authorize() RETURNS boolean LANGUAGE sql AS $$SELECT false$$`); e == nil {
		t.Fatal("restricted owner unexpectedly replaced function")
	}
	for _, prepare := range setup {
		prepare(db)
	}
	manifest, e := Catalog(context.Background(), db, "public")
	if e != nil {
		t.Fatal(e)
	}
	ExpectedCatalogSHA256 = manifest.SHA256
	c.Database.SchemaSHA256 = manifest.SHA256
	// The old fixture seal is immutable. Its initial catalog string is supplied
	// by an insert, never an UPDATE to a historical seal.
	adminDB, e := sql.Open("pgx", adminURL.String())
	if e != nil {
		t.Fatal(e)
	}
	exec(adminDB, `ALTER TABLE whatomate_schema_seal DISABLE TRIGGER USER`)
	if _, e = adminDB.Exec(`INSERT INTO whatomate_schema_seal(generation,release,catalog_sha256) OVERRIDING SYSTEM VALUE VALUES(4,$1,$2)`, r.PreviousRelease, manifest.SHA256); e != nil {
		t.Fatal(e)
	}
	exec(adminDB, `ALTER TABLE whatomate_schema_seal ENABLE TRIGGER USER`)
	adminDB.Close()
	return db, c, r
}

func compatibleACL(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, e := db.Query(`SELECT 'relation:'||c.oid::text||':'||c.relowner::text||':'||coalesce(c.relacl::text,'') FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='public'
 UNION ALL SELECT 'column:'||a.attrelid::text||':'||a.attnum::text||':'||coalesce(a.attacl::text,'') FROM pg_attribute a JOIN pg_class c ON a.attrelid=c.oid JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='public' AND a.attnum>0
 UNION ALL SELECT 'function:'||p.oid::text||':'||p.proowner::text||':'||coalesce(p.proacl::text,'') FROM pg_proc p JOIN pg_namespace n ON p.pronamespace=n.oid WHERE n.nspname='public'
 UNION ALL SELECT 'namespace:'||oid::text||':'||nspowner::text||':'||coalesce(nspacl::text,'') FROM pg_namespace WHERE nspname='public'
 ORDER BY 1`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var value string
		if e = rows.Scan(&value); e != nil {
			t.Fatal(e)
		}
		out.WriteString(value)
		out.WriteByte('\n')
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return out.String()
}

func TestCompatiblePGPreservesCatalogAndNOLOGINFunctionACL(t *testing.T) {
	db, c, r := compatiblePG(t)
	before := compatibleACL(t, db)
	manifest, result, e := SealCompatible(context.Background(), db, c, r, false)
	if e != nil {
		t.Fatal(e)
	}
	if result.Sealed || result.DDLExecuted || result.MigrationsExecuted || manifest.SHA256 != ExpectedCatalogSHA256 {
		t.Fatal("preview mutated or mismatched")
	}
	var attempts int
	db.QueryRow(`SELECT count(*) FROM whatomate_schema_attempt`).Scan(&attempts)
	if attempts != 0 {
		t.Fatal("preview reserved attempt")
	}
	manifest, result, e = SealCompatible(context.Background(), db, c, r, true)
	if e != nil {
		t.Fatal(e)
	}
	if !result.Sealed || result.Generation != 5 || result.RuntimeStarted || !result.DispatchHeld || result.DDLExecuted || result.MigrationsExecuted {
		t.Fatal("incorrect successor receipt")
	}
	if manifest.Release != ReleaseIdentity || compatibleACL(t, db) != before {
		t.Fatal("catalog source or ACL changed")
	}
	after, e := Catalog(context.Background(), db, "public")
	if e != nil || after.SHA256 != manifest.SHA256 {
		t.Fatal("physical shape changed")
	}
	if _, _, e = SealCompatible(context.Background(), db, c, r, true); e == nil {
		t.Fatal("seal operation retried")
	}
}

func TestCompatiblePGRefusesLeaseDriftPendingAndWrongHistory(t *testing.T) {
	db, c, r := compatiblePG(t)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = conn.ExecContext(context.Background(), `SELECT pg_advisory_lock_shared($1)`, LockID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = SealCompatible(context.Background(), db, c, r, true); e == nil {
		t.Fatal("shared runtime lease bypassed")
	}
	conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock_shared($1)`, LockID)
	conn.Close()
	for _, change := range []func(CompatibleRequest) CompatibleRequest{func(v CompatibleRequest) CompatibleRequest { v.PreviousGeneration = 3; return v }, func(v CompatibleRequest) CompatibleRequest {
		v.PreviousRelease = strings.Repeat("e", 40) + ":" + strings.Repeat("b", 40)
		return v
	}} {
		if _, _, e = SealCompatible(context.Background(), db, c, change(r), true); e == nil {
			t.Fatal("wrong source history accepted")
		}
	}
	if _, e = db.Exec(`CREATE TABLE incompatible_extra(id bigint)`); e != nil {
		t.Fatal(e)
	}
	if _, _, e = SealCompatible(context.Background(), db, c, r, true); e == nil {
		t.Fatal("physical drift accepted")
	}
	db.Exec(`DROP TABLE incompatible_extra`)
	if _, e = db.Exec(`INSERT INTO whatomate_schema_attempt VALUES('unsealed-test','not-baseline')`); e != nil {
		t.Fatal(e)
	}
	if _, _, e = SealCompatible(context.Background(), db, c, r, true); e == nil {
		t.Fatal("pending attempt accepted")
	}
	var attempts int
	db.QueryRow(`SELECT count(*) FROM whatomate_schema_attempt WHERE release=$1`, ReleaseIdentity).Scan(&attempts)
	if attempts != 0 {
		t.Fatal("refusal created target attempt")
	}
}

func TestCompatiblePGOneConcurrentSealOnly(t *testing.T) {
	db, c, r := compatiblePG(t)
	var wait sync.WaitGroup
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, _, e := SealCompatible(context.Background(), db, c, r, true); out <- e }()
	}
	wait.Wait()
	close(out)
	success := 0
	for e := range out {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successes=%d", success)
	}
}

func TestCompatiblePGCommitFailureLeavesDurableAttempt(t *testing.T) {
	db, c, r := compatiblePG(t, func(db *sql.DB) {
		if _, e := db.Exec(`CREATE FUNCTION compatible_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic deferred commit failure';END$$;
 CREATE CONSTRAINT TRIGGER compatible_commit_failure AFTER INSERT ON whatomate_schema_seal DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION compatible_commit_failure();`); e != nil {
			t.Fatal(e)
		}
	})
	_, _, e := SealCompatible(context.Background(), db, c, r, true)
	if !errors.Is(e, ErrCommitUnknown) {
		t.Fatalf("commit failure did not keep held: %v", e)
	}
	var attempts, seals int
	db.QueryRow(`SELECT count(*) FROM whatomate_schema_attempt WHERE release=$1`, ReleaseIdentity).Scan(&attempts)
	db.QueryRow(`SELECT count(*) FROM whatomate_schema_seal WHERE release=$1`, ReleaseIdentity).Scan(&seals)
	if attempts != 1 || seals != 0 {
		t.Fatalf("durable attempt=%d successor seals=%d", attempts, seals)
	}
	if _, _, e = SealCompatible(context.Background(), db, c, r, true); e == nil {
		t.Fatal("interrupted operation retried")
	}
}

// Accepted installations can retain their original bootstrap superuser. The
// operation must neither downgrade that role nor change any observed ACL.
func TestCompatiblePGBootstrapOwnerAndNonOwner(t *testing.T) {
	db, c, r := compatiblePG(t)
	adminURL, _ := url.Parse(os.Getenv("EXTENSION_COMPATIBLE_TEST_DSN"))
	adminURL.Path = "/" + c.Database.Name
	admin, e := sql.Open("pgx", adminURL.String())
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	if _, e = admin.Exec("ALTER ROLE " + c.Database.User + " SUPERUSER"); e != nil {
		t.Fatal(e)
	}
	before := compatibleACL(t, db)
	outsider := c.Database.User + "_other"
	if _, e = admin.Exec("CREATE ROLE " + outsider + " LOGIN SUPERUSER"); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec("DROP ROLE " + outsider)
	otherURL := *adminURL
	otherURL.User = url.User(outsider)
	other, e := sql.Open("pgx", otherURL.String())
	if e != nil {
		t.Fatal(e)
	}
	bad := *c
	bad.Database.User = outsider
	if _, _, e = SealCompatible(context.Background(), other, &bad, r, true); e == nil {
		t.Fatal("non-owner superuser accepted")
	}
	other.Close()
	if _, _, e = SealCompatible(context.Background(), db, c, r, true); e != nil {
		t.Fatal(e)
	}
	var super bool
	if e = db.QueryRow("SELECT rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&super); e != nil || !super || compatibleACL(t, db) != before {
		t.Fatal("bootstrap role or ACL changed", e)
	}
}

func TestCompatiblePGApplyLocksAuthenticatedRows(t *testing.T) {
	db, c, r := compatiblePG(t)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	for _, own := range []bool{false, true} {
		if own {
			if _, e = db.Exec("INSERT INTO whatomate_schema_attempt(release,catalog_sha256) VALUES($1,$2)", ReleaseIdentity, ExpectedCatalogSHA256); e != nil {
				t.Fatal(e)
			}
		}
		tx, _, e := compatibleTransaction(context.Background(), conn, c, r, own, false)
		if e != nil {
			t.Fatal(e)
		}
		for _, statement := range []string{
			"UPDATE whatomate_protection_state SET protected=false WHERE id=1",
			"UPDATE organizations SET deleted_at=now()",
			"UPDATE users SET is_active=false",
			"UPDATE whatomate_schema_seal SET catalog_sha256='changed' WHERE generation=4",
		} {
			other, e := db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			if _, e = other.Exec("SET LOCAL lock_timeout='100ms'"); e != nil {
				t.Fatal(e)
			}
			if _, e = other.Exec(statement); e == nil {
				other.Rollback()
				tx.Rollback()
				t.Fatalf("authenticated row not locked: %s", statement)
			}
			other.Rollback()
		}
		tx.Rollback()
	}
}
