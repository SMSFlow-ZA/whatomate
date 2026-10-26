package main

import (
	"context"
	"database/sql"
	"encoding/json"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"golang.org/x/crypto/bcrypt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*sql.DB, *config.Config, Plan, string) { return fixtureAt(t, false) }
func fixtureAt(t *testing.T, public bool) (*sql.DB, *config.Config, Plan, string) {
	t.Helper()
	dsn := os.Getenv("DEMO_IDENTITIES_TEST_DSN")
	if dsn == "" {
		t.Fatal("disposable DEMO_IDENTITIES_TEST_DSN required")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	ns := "demo_identities_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if public {
		ns = "public"
	} else if _, e = admin.Exec("CREATE SCHEMA " + ns); e != nil {
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
	p := Plan{Version: 1, InstanceID: "11111111-1111-4111-8111-111111111111", OrganizationID: uuid.NewString(), OperatorID: uuid.NewString(), Generation: 5, Release: strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40), Purpose: "synthetic-role-acceptance", AttemptNonce: strings.Repeat("c", 32)}
	for _, label := range fixtureLabels {
		p.Identities = append(p.Identities, Identity{Fixture: label, UserID: uuid.NewString(), MembershipID: uuid.NewString(), Email: "synthetic-role-" + label + "-1234abcd@example.invalid"})
	}
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
	if _, e = db.Exec(`INSERT INTO user_organizations(id,user_id,organization_id,is_default) VALUES($1,$2,$3,true)`, uuid.NewString(), p.OperatorID, p.OrganizationID); e != nil {
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
	_, e = db.Exec(`INSERT INTO whatomate_schema_seal VALUES(5,$1,$2);`, p.Release, p.CatalogSHA256)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO whatomate_protection_state VALUES(1,true)`); e != nil {
		t.Fatal(e)
	}
	return db, c, p, ns
}

func passwords(p Plan) map[string][]byte {
	out := map[string][]byte{}
	for _, v := range p.Identities {
		out[v.Fixture] = []byte("synthetic-fixture-only-private-password-123456")
	}
	return out
}
func TestPreviewCreatesNothing(t *testing.T) {
	db, c, p, ns := fixture(t)
	r, e := provision(context.Background(), db, c, p, ns, nil, false, strings.Repeat("d", 64))
	if e != nil || r.Applied {
		t.Fatal("preview refused", e)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM users").Scan(&n)
	if n != 1 {
		t.Fatal("preview wrote users")
	}
}
func TestAtomicSixNewUnprivilegedIdentities(t *testing.T) {
	db, c, p, ns := fixture(t)
	pw := passwords(p)
	r, e := provision(context.Background(), db, c, p, ns, pw, true, strings.Repeat("d", 64))
	if e != nil || !r.Applied || r.Outcome != "committed-observed" {
		t.Fatal("creation refused", e)
	}
	for _, v := range p.Identities {
		var hash, name string
		var boundary bool
		e = db.QueryRow(`SELECT password_hash,full_name,is_active AND NOT is_available AND NOT is_super_admin AND role_id IS NULL AND sso_provider='' AND sso_binding='' AND sso_provider_id='' FROM users WHERE id=$1 AND organization_id=$2`, v.UserID, p.OrganizationID).Scan(&hash, &name, &boundary)
		if e != nil || !boundary || name != "SYNTHETIC ROLE ACCEPTANCE: "+v.Fixture || bcrypt.CompareHashAndPassword([]byte(hash), pw[v.Fixture]) != nil {
			t.Fatal("identity boundary", e)
		}
		var n int
		db.QueryRow(`SELECT count(*) FROM user_organizations WHERE id=$1 AND user_id=$2 AND organization_id=$3 AND role_id IS NULL AND is_default AND deleted_at IS NULL`, v.MembershipID, v.UserID, p.OrganizationID).Scan(&n)
		if n != 1 {
			t.Fatal("membership boundary")
		}
		var changes []byte
		db.QueryRow("SELECT changes FROM audit_logs WHERE resource_id=$1", v.UserID).Scan(&changes)
		if !json.Valid(changes) || strings.Contains(string(changes), string(pw[v.Fixture])) || strings.Contains(string(changes), hash) {
			t.Fatal("unsafe audit")
		}
	}
	var n int
	db.QueryRow("SELECT (SELECT count(*) FROM addon_api_grants)+(SELECT count(*) FROM addon_api_identity_links)+(SELECT count(*) FROM extension_security_mfa)+(SELECT count(*) FROM api_keys)+(SELECT count(*) FROM addon_knowledge_grants)").Scan(&n)
	if n != 0 {
		t.Fatal("identity creation granted domain authority")
	}
	if _, e = provision(context.Background(), db, c, p, ns, pw, true, strings.Repeat("d", 64)); e == nil {
		t.Fatal("existing identities overwritten")
	}
}

func TestPreconditionsAndNewOnlyRefuseWithoutWrites(t *testing.T) {
	for _, kind := range []string{"purpose", "tenant", "operator", "membership", "generation", "pending", "protected", "catalog", "database", "email", "user", "membership-id", "api-key", "mfa", "passwords"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, ns := fixture(t)
			pw := passwords(p)
			id := p.Identities[0]
			switch kind {
			case "purpose":
				p.Purpose = "employee-onboarding"
			case "tenant":
				p.OrganizationID = uuid.NewString()
				db.Exec("INSERT INTO organizations VALUES($1,NULL)", p.OrganizationID)
			case "operator":
				p.OperatorID = uuid.NewString()
			case "membership":
				db.Exec("UPDATE user_organizations SET is_default=false WHERE user_id=$1", p.OperatorID)
			case "generation":
				p.Generation = 4
			case "pending":
				db.Exec("INSERT INTO whatomate_schema_attempt VALUES('pending','unsealed')")
			case "protected":
				db.Exec("UPDATE whatomate_protection_state SET protected=false")
			case "catalog":
				db.Exec("CREATE TABLE unexpected_physical_drift(id int)")
			case "database":
				c.Database.Name = "wrong_database"
			case "email":
				db.Exec("INSERT INTO users(id,organization_id,email)VALUES($1,$2,$3)", uuid.NewString(), uuid.NewString(), id.Email)
			case "user":
				db.Exec("INSERT INTO users(id,email)VALUES($1,'existing@example.invalid')", id.UserID)
			case "membership-id":
				db.Exec("INSERT INTO user_organizations(id,user_id,organization_id)VALUES($1,$2,$3)", id.MembershipID, uuid.NewString(), p.OrganizationID)
			case "api-key":
				db.Exec("INSERT INTO api_keys VALUES($1)", id.UserID)
			case "mfa":
				db.Exec("INSERT INTO extension_security_mfa VALUES($1)", id.UserID)
			case "passwords":
				delete(pw, "manager")
			}
			if _, e := provision(context.Background(), db, c, p, ns, pw, true, strings.Repeat("d", 64)); e == nil {
				t.Fatal("unsafe creation accepted")
			}
			var n int
			db.QueryRow("SELECT count(*) FROM audit_logs").Scan(&n)
			if n != 0 {
				t.Fatal("refusal wrote audits")
			}
			for _, v := range p.Identities[1:] {
				db.QueryRow("SELECT count(*) FROM users WHERE id=$1", v.UserID).Scan(&n)
				if n != 0 {
					t.Fatal("partial identity survived")
				}
			}
		})
	}
}
func TestQuotaAndAuditFailureRollBackEntireBatch(t *testing.T) {
	for _, kind := range []string{"quota", "audit"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, ns := fixture(t)
			statement := `CREATE FUNCTION reject_later() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.full_name='SYNTHETIC ROLE ACCEPTANCE: hrbp' THEN PERFORM nextval('failure_stage');RAISE EXCEPTION 'synthetic quota exhausted'; END IF;RETURN NEW;END $$;CREATE TRIGGER preserved_user_quota BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION reject_later()`
			if kind == "audit" {
				statement = `CREATE FUNCTION reject_later() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM audit_logs)>=2 THEN PERFORM nextval('failure_stage');RAISE EXCEPTION 'synthetic audit failure'; END IF;RETURN NEW;END $$;CREATE TRIGGER audit_failure BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_later()`
			}
			if _, e := db.Exec("CREATE SEQUENCE failure_stage"); e != nil {
				t.Fatal(e)
			}
			if _, e := db.Exec(statement); e != nil {
				t.Fatal(e)
			}
			// Installing test triggers changes the physical catalog: seal that actual fixture, never bypass catalog validation.
			catalog, e := protectedschema.Catalog(context.Background(), db, ns)
			if e != nil {
				t.Fatal(e)
			}
			p.CatalogSHA256 = catalog.SHA256
			protectedschema.ExpectedCatalogSHA256 = catalog.SHA256
			c.Database.SchemaSHA256 = catalog.SHA256
			db.Exec("UPDATE whatomate_schema_seal SET catalog_sha256=$1", catalog.SHA256)
			if _, e = provision(context.Background(), db, c, p, ns, passwords(p), true, strings.Repeat("d", 64)); e == nil {
				t.Fatal("trigger failure accepted")
			}
			var reached bool
			if e = db.QueryRow("SELECT is_called FROM failure_stage").Scan(&reached); e != nil || !reached {
				t.Fatal("failure trigger was not actually reached", e)
			}
			var n int
			db.QueryRow("SELECT count(*) FROM users").Scan(&n)
			if n != 1 {
				t.Fatal("earlier created users survived rollback")
			}
			db.QueryRow("SELECT count(*) FROM user_organizations").Scan(&n)
			if n != 1 {
				t.Fatal("memberships survived rollback")
			}
			db.QueryRow("SELECT count(*) FROM audit_logs").Scan(&n)
			if n != 0 {
				t.Fatal("audits survived rollback")
			}
		})
	}
}
func TestReadOnlyReconcileRequiresExactAuditAndEmptyAuthority(t *testing.T) {
	for _, kind := range []string{"valid", "password", "audit", "role", "factor", "cross-membership"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, ns := fixture(t)
			pw := passwords(p)
			if _, e := provision(context.Background(), db, c, p, ns, pw, true, strings.Repeat("d", 64)); e != nil {
				t.Fatal(e)
			}
			id := p.Identities[0]
			switch kind {
			case "password":
				pw[id.Fixture] = []byte("wrong-private-password-with-enough-length-1234")
			case "audit":
				db.Exec("UPDATE audit_logs SET changes='[]'::jsonb WHERE resource_id=$1", id.UserID)
			case "role":
				db.Exec("UPDATE users SET role_id=$1 WHERE id=$2", uuid.NewString(), id.UserID)
			case "factor":
				db.Exec("INSERT INTO extension_security_mfa VALUES($1)", id.UserID)
			case "cross-membership":
				db.Exec("INSERT INTO user_organizations(id,user_id,organization_id)VALUES($1,$2,$3)", uuid.NewString(), id.UserID, uuid.NewString())
			}
			r, e := reconcile(context.Background(), db, c, p, ns, pw, strings.Repeat("d", 64))
			if kind == "valid" {
				if e != nil || r.Outcome != "committed-observed" || r.Applied {
					t.Fatal("readonly reconciliation failed", e)
				}
			} else if e == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestNewIdentitiesHaveNoDomainAuthorization(t *testing.T) {
	db, c, p, ns := fixture(t)
	if _, e := provision(context.Background(), db, c, p, ns, passwords(p), true, strings.Repeat("d", 64)); e != nil {
		t.Fatal(e)
	}
	for _, identity := range p.Identities {
		a := addonapi.Actor{OrganizationID: p.OrganizationID, UserID: identity.UserID, SessionID: "synthetic-trusted-host-boundary", PrimarySessionAuthorized: true}
		r := httptest.NewRequest("GET", "/api/extensions/cases/", nil)
		r = r.WithContext(addonapi.WithActor(r.Context(), a))
		if _, e := addonapi.ActorFrom(r.Context()); e != nil {
			t.Fatal("protected demo authentication fixture invalid")
		}
		for _, permission := range []string{"cases:read.own", "cases:read.triage", "knowledge:read", "reports:aggregate", "directory:read"} {
			if _, e := addonapi.Require(r, permission); e == nil {
				t.Fatal("ungranted identity authorized", identity.Fixture, permission)
			}
		}
	}
}

func TestConcurrentUniqueEmailsPermitOnlyOneCompleteBatch(t *testing.T) {
	db, c, p, ns := fixture(t)
	other := p
	other.Identities = append([]Identity(nil), p.Identities...)
	other.AttemptNonce = strings.Repeat("f", 32)
	for i := range other.Identities {
		other.Identities[i].UserID = uuid.NewString()
		other.Identities[i].MembershipID = uuid.NewString()
	}
	results := make(chan bool, 2)
	for _, plan := range []Plan{p, other} {
		go func(plan Plan) {
			r, e := provision(context.Background(), db, c, plan, ns, passwords(plan), true, strings.Repeat("d", 64))
			results <- e == nil && r.Applied
		}(plan)
	}
	winners := 0
	for i := 0; i < 2; i++ {
		if <-results {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("expected exactly one atomic creation", winners)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM users").Scan(&n)
	if n != 7 {
		t.Fatal("partial concurrent creation")
	}
	db.QueryRow("SELECT count(*) FROM audit_logs").Scan(&n)
	if n != 6 {
		t.Fatal("concurrent audit inventory")
	}
}
func TestCommitDisconnectIsUnknownHeldWithoutRetry(t *testing.T) {
	db, c, p, ns := fixture(t)
	if _, e := db.Exec(`CREATE FUNCTION disconnect_at_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.full_name='SYNTHETIC ROLE ACCEPTANCE: employee' THEN PERFORM pg_terminate_backend(pg_backend_pid());END IF;RETURN NEW;END $$;CREATE CONSTRAINT TRIGGER deferred_disconnect AFTER INSERT ON users DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION disconnect_at_commit()`); e != nil {
		t.Fatal(e)
	}
	catalog, e := protectedschema.Catalog(context.Background(), db, ns)
	if e != nil {
		t.Fatal(e)
	}
	p.CatalogSHA256 = catalog.SHA256
	protectedschema.ExpectedCatalogSHA256 = catalog.SHA256
	c.Database.SchemaSHA256 = catalog.SHA256
	db.Exec("UPDATE whatomate_schema_seal SET catalog_sha256=$1", catalog.SHA256)
	r, e := provision(context.Background(), db, c, p, ns, passwords(p), true, strings.Repeat("d", 64))
	if e == nil || r.Applied || r.Outcome != "unknown-held" {
		t.Fatal("ambiguous COMMIT not held", r.Outcome, e)
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM users").Scan(&n); e != nil || n != 1 {
		t.Fatal("unexpected retained fixture state", n, e)
	}
}
