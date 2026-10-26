package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"golang.org/x/crypto/bcrypt"
)

func fixture(t *testing.T) (*sql.DB, *config.Config, Plan, string) {
	t.Helper()
	dsn := os.Getenv("PUBLISHER_TEST_DSN")
	if dsn == "" {
		t.Fatal("disposable PUBLISHER_TEST_DSN required")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	ns := "publisher_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec("CREATE SCHEMA " + ns); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", ns)
	u.RawQuery = q.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(3)
	t.Cleanup(func() { db.Close(); admin.Exec("DROP SCHEMA " + ns + " CASCADE"); admin.Close() })
	_, e = db.Exec(`CREATE TABLE organizations(id uuid PRIMARY KEY,deleted_at timestamptz);
 CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid,email text UNIQUE,auth_stamp text,password_hash text,full_name text,role_id uuid,settings jsonb,is_active boolean,is_available boolean,is_super_admin boolean,sso_provider text,sso_binding text,sso_provider_id text,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz);
 CREATE TABLE user_organizations(id uuid PRIMARY KEY,user_id uuid,organization_id uuid,role_id uuid,is_default boolean,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz);
 CREATE TABLE audit_logs(id uuid PRIMARY KEY,organization_id uuid,resource_type text,resource_id uuid,user_id uuid,user_name text,action text,changes jsonb,created_at timestamptz);
 CREATE TABLE api_keys(user_id uuid);CREATE TABLE addon_api_identity_links(user_id text);CREATE TABLE addon_api_grants(user_id text);CREATE TABLE addon_api_access_versions(user_id text);CREATE TABLE addon_api_access_audit(user_id text);CREATE TABLE extension_security_mfa(user_id text);CREATE TABLE addon_knowledge_grants(principal text);
 CREATE TABLE whatomate_schema_seal(generation bigint,release text,catalog_sha256 text);
 CREATE TABLE whatomate_schema_attempt(release text,catalog_sha256 text);
 CREATE TABLE whatomate_protection_state(id integer,protected boolean);`)
	if e != nil {
		t.Fatal(e)
	}
	p := Plan{Version: 1, InstanceID: "11111111-1111-4111-8111-111111111111", OrganizationID: uuid.NewString(), OperatorID: uuid.NewString(), UserID: uuid.NewString(), MembershipID: uuid.NewString(), Email: "synthetic-knowledge-publisher-1234abcd@example.invalid", Generation: 4, Release: strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40), Purpose: "automation-synthetic-knowledge-publisher"}
	c := &config.Config{}
	c.Extensions.ProtectedMode = true
	c.Database.SchemaManagedExternally = true
	c.Database.SchemaRelease = p.Release
	c.App.Environment = "production"
	if e = db.QueryRow(`SELECT current_database(),current_user`).Scan(&c.Database.Name, &c.Database.User); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO organizations VALUES($1,NULL)`, p.OrganizationID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO users(id,organization_id,email,full_name,is_active,is_super_admin,password_hash)VALUES($2,$1,'owner@example.invalid','Owner',true,true,'owner-unchanged')`, p.OrganizationID, p.OperatorID); e != nil {
		t.Fatal(e)
	}
	oldR, oldS := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity = oldR; protectedschema.ExpectedCatalogSHA256 = oldS })
	protectedschema.ReleaseIdentity = p.Release
	m, e := protectedschema.Catalog(context.Background(), db, ns)
	if e != nil {
		t.Fatal(e)
	}
	p.CatalogSHA256 = m.SHA256
	protectedschema.ExpectedCatalogSHA256 = m.SHA256
	c.Database.SchemaSHA256 = m.SHA256
	_, e = db.Exec(`INSERT INTO whatomate_schema_seal VALUES(4,$1,$2);`, p.Release, p.CatalogSHA256)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO whatomate_protection_state VALUES(1,true)`); e != nil {
		t.Fatal(e)
	}
	return db, c, p, ns
}
func TestPreviewCreatesNothing(t *testing.T) {
	db, c, p, ns := fixture(t)
	r, e := provision(context.Background(), db, c, p, ns, nil, false)
	if e != nil || r.Applied {
		t.Fatal("preview refused", e)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Fatal("preview wrote users")
	}
}
func TestCreateOnlySyntheticUnprivilegedIdentityAndAudit(t *testing.T) {
	db, c, p, ns := fixture(t)
	password := []byte("fixture-only-password-with-high-entropy-1234")
	r, e := provision(context.Background(), db, c, p, ns, password, true)
	if e != nil || !r.Applied {
		t.Fatal("create refused", e)
	}
	var hash, name string
	var active, available, super, emptyRole bool
	e = db.QueryRow(`SELECT password_hash,full_name,is_active,is_available,is_super_admin,role_id IS NULL FROM users WHERE id=$1`, p.UserID).Scan(&hash, &name, &active, &available, &super, &emptyRole)
	if e != nil || bcrypt.CompareHashAndPassword([]byte(hash), password) != nil || !active || available || super || !emptyRole || name != syntheticName {
		t.Fatal("identity boundary", e)
	}
	var unchanged string
	db.QueryRow(`SELECT password_hash FROM users WHERE id=$1`, p.OperatorID).Scan(&unchanged)
	if unchanged != "owner-unchanged" {
		t.Fatal("owner changed")
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM user_organizations WHERE user_id=$1 AND role_id IS NULL AND is_default`, p.UserID).Scan(&n)
	if n != 1 {
		t.Fatal("membership missing")
	}
	var raw []byte
	db.QueryRow(`SELECT changes FROM audit_logs WHERE resource_id=$1`, p.UserID).Scan(&raw)
	if !json.Valid(raw) || strings.Contains(string(raw), string(password)) || strings.Contains(string(raw), hash) || !strings.Contains(string(raw), "automation") {
		t.Fatal("audit missing or secret-bearing")
	}
	if _, e = provision(context.Background(), db, c, p, ns, password, true); e == nil {
		t.Fatal("existing identity overwritten")
	}
}
func TestRefusalBoundaries(t *testing.T) {
	for _, kind := range []string{"owner-target", "real-email", "wrong-generation", "wrong-release", "wrong-catalog", "inactive-owner", "pending-schema", "unprotected", "collision-email", "orphan-grant", "orphan-identity", "wrong-database", "wrong-owner", "wrong-live-shape"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, ns := fixture(t)
			switch kind {
			case "owner-target":
				p.UserID = p.OperatorID
			case "real-email":
				p.Email = "person@real.example"
			case "wrong-generation":
				p.Generation++
			case "wrong-release":
				p.Release = strings.Repeat("c", 40) + ":" + strings.Repeat("d", 40)
			case "wrong-catalog":
				p.CatalogSHA256 = strings.Repeat("0", 64)
			case "inactive-owner":
				db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, p.OperatorID)
			case "pending-schema":
				db.Exec(`INSERT INTO whatomate_schema_attempt VALUES('pending','other')`)
			case "unprotected":
				db.Exec(`UPDATE whatomate_protection_state SET protected=false`)
			case "orphan-grant":
				db.Exec(`INSERT INTO addon_api_grants VALUES($1)`, p.UserID)
			case "orphan-identity":
				db.Exec(`INSERT INTO addon_api_identity_links VALUES($1)`, p.UserID)
			case "wrong-database":
				c.Database.Name = "other"
			case "wrong-owner":
				c.Database.User = "other"
			case "wrong-live-shape":
				db.Exec(`ALTER TABLE users ADD COLUMN unexpected text`)
			case "collision-email":
				db.Exec(`UPDATE users SET email=$1 WHERE id=$2`, p.Email, p.OperatorID)
			}
			if _, e := provision(context.Background(), db, c, p, ns, []byte("fixture-only-long-password-12345678901234567890"), true); e == nil {
				t.Fatal("unsafe identity accepted")
			}
			var n int
			db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
			if n != 1 {
				t.Fatal("refusal wrote identity")
			}
		})
	}
}
func TestExclusiveSchemaLeaseBlocksProvision(t *testing.T) {
	db, c, p, ns := fixture(t)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, protectedschema.LockID); e != nil {
		t.Fatal(e)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, protectedschema.LockID)
	if _, e = provision(context.Background(), db, c, p, ns, []byte("fixture-only-long-password-12345678901234567890"), true); e == nil {
		t.Fatal("owner installer lease bypassed")
	}
}
