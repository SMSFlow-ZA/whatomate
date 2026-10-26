package main

import (
	"context"
	"database/sql"
	"io"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"github.com/shridarpatil/whatomate/pkg/extensions/processingauth"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/zerodha/logf"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const fixturePassword = "disposable-only-not-production"

func newFixtureDatabase(t *testing.T, admin *sql.DB, dsn string) *sql.DB {
	t.Helper()
	u, e := url.Parse(dsn)
	if e != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("URL DSN required")
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "worker-bootstrap-pg" {
		t.Fatal("only explicitly local disposable server accepted")
	}
	database, owner, worker, fnowner := fixturePlan().names()
	for _, st := range []string{"CREATE ROLE " + owner + " LOGIN SUPERUSER PASSWORD '" + fixturePassword + "'", "CREATE ROLE " + worker + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + fixturePassword + "'", "CREATE DATABASE " + database + " OWNER " + owner} {
		if _, e = admin.Exec(st); e != nil {
			t.Fatal("disposable namespace unavailable; never adopted or deleted")
		}
	}
	u.User = url.UserPassword(owner, fixturePassword)
	u.Path = "/" + database
	q := u.Query()
	q.Set("search_path", "public")
	u.RawQuery = q.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal("fixture owner unavailable")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec("DROP DATABASE " + database)
		for _, r := range []string{fnowner, worker, owner} {
			_, _ = admin.Exec("DROP ROLE IF EXISTS " + r)
		}
		admin.Close()
	})
	if _, e = db.Exec("ALTER SCHEMA public OWNER TO " + owner); e != nil {
		t.Fatal(e)
	}
	return db
}

const fixtureDDL = `
CREATE TABLE public.organizations(id uuid PRIMARY KEY,deleted_at timestamptz);
CREATE TABLE public.users(id uuid PRIMARY KEY,organization_id uuid NOT NULL,email text UNIQUE NOT NULL,auth_stamp text,password_hash text,full_name text,role_id uuid,is_active boolean,is_super_admin boolean,sso_provider text,sso_provider_id text,sso_binding text,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz);
CREATE TABLE public.user_organizations(id uuid PRIMARY KEY,user_id uuid,organization_id uuid,role_id uuid,is_default boolean,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz);
CREATE TABLE public.custom_roles(id uuid PRIMARY KEY,organization_id uuid,name text,description text,is_system boolean,is_default boolean,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz);
CREATE TABLE public.permissions(id uuid PRIMARY KEY,resource text,action text,description text,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz,UNIQUE(resource,action));
CREATE TABLE public.role_permissions(custom_role_id uuid,permission_id uuid,PRIMARY KEY(custom_role_id,permission_id));
CREATE TABLE public.api_keys(user_id uuid);
CREATE TABLE public.extension_security_mfa(organization_id text,user_id text);
CREATE TABLE public.whatomate_schema_seal(generation bigint,release text,catalog_sha256 text);
CREATE TABLE public.whatomate_schema_attempt(release text,catalog_sha256 text);
CREATE TABLE public.whatomate_protection_state(id smallint PRIMARY KEY,protected boolean);
INSERT INTO public.whatomate_protection_state VALUES(1,true);
CREATE TABLE public.extension_schema(id text,checksum text);
CREATE TABLE public.extension_security_schema(id text,checksum text);
CREATE TABLE public.addon_cases_schema(version integer);
CREATE FUNCTION public.fixture_quota() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM public.users)>=3 THEN RAISE EXCEPTION 'fixture user quota'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fixture_quota BEFORE INSERT ON public.users FOR EACH ROW EXECUTE FUNCTION public.fixture_quota();`

func prepareFixture(t *testing.T, db *sql.DB) Plan {
	t.Helper()
	p := fixturePlan()
	ctx := context.Background()
	if _, e := db.Exec(fixtureDDL); e != nil {
		t.Fatal(e)
	}
	if e := (api.AccessStore{DB: db}).Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e := knowledge.MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	if e := processingauth.Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO public.organizations(id) VALUES($1)`, p.OrganizationID); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO public.users(id,organization_id,email,is_active,is_super_admin) VALUES($1,$2,'fixture-operator@example.invalid',true,false)`, p.OperatorID, p.OrganizationID); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO public.custom_roles(id,organization_id,name,is_system,is_default) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',$1,'agent',true,true)`, p.OrganizationID); e != nil {
		t.Fatal(e)
	}
	refreshPins(t, db, &p)
	if _, e := db.Exec(`INSERT INTO public.whatomate_schema_seal VALUES($1,$2,$3)`, p.Generation, p.Release, p.CatalogSHA256); e != nil {
		t.Fatal(e)
	}
	return p
}
func refreshPins(t *testing.T, db *sql.DB, p *Plan) {
	t.Helper()
	m, e := protectedschema.Catalog(context.Background(), db, "public")
	if e != nil {
		t.Fatal(e)
	}
	p.CatalogSHA256 = m.SHA256
	var definition string
	if db.QueryRow(`SELECT pg_get_functiondef(to_regprocedure($1))`, signature).Scan(&definition) != nil {
		t.Fatal("function unavailable")
	}
	p.FunctionSHA256 = digest([]byte(definition))
	p.BeforeACL, e = captureACL(context.Background(), db)
	if e != nil {
		rows, diagnostic := db.QueryContext(context.Background(), ACLObservationSQL)
		if diagnostic != nil {
			t.Fatalf("synthetic ACL projection SQL: %v", diagnostic)
		}
		defer rows.Close()
		for rows.Next() {
			var r ACLRecord
			var grants string
			if diagnostic = rows.Scan(&r.Kind, &r.Object, &r.Owner, &r.Explicit, &grants, &r.Detail); diagnostic != nil {
				t.Fatalf("synthetic ACL projection scan: %v", diagnostic)
			}
		}
		t.Fatalf("synthetic ACL observation validation refused: %v", rows.Err())
	}
	_, _ = db.Exec(`UPDATE public.whatomate_schema_seal SET catalog_sha256=$1`, p.CatalogSHA256)
}
func fixtureSession(t *testing.T, db *sql.DB, p Plan) *Session {
	t.Helper()
	var pid int
	var held bool
	if db.QueryRow(`SELECT pg_backend_pid(),pg_try_advisory_lock($1)`, protectedschema.LockID).Scan(&pid, &held) != nil || !held {
		t.Fatal("lease unavailable")
	}
	database, owner, _, _ := p.names()
	return &Session{DB: db, PID: pid, Owner: owner, Database: database}
}
func assertAbsent(t *testing.T, db *sql.DB, p Plan) {
	t.Helper()
	if absent(context.Background(), db, p) != nil {
		t.Fatal("partial bootstrap survived rollback")
	}
	actual, e := captureACL(context.Background(), db)
	if e != nil || !observationsEqual(actual, p.BeforeACL) {
		t.Fatal("ACL survived rollback")
	}
}
func assertCredentiallessAuthentication(t *testing.T, db *sql.DB, p Plan) {
	t.Helper()
	g, e := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	cache := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Millisecond, ReadTimeout: time.Millisecond, WriteTimeout: time.Millisecond, MaxRetries: -1})
	defer cache.Close()
	app := &handlers.App{DB: g, Redis: cache, Config: &config.Config{}, Log: logf.New(logf.Opts{Writer: io.Discard})}
	for _, password := range []string{"anything-valid-123", fixturePassword} {
		req := testutil.NewJSONRequest(t, map[string]any{"email": p.Email, "password": password})
		if app.Login(req) != nil || testutil.GetResponseStatusCode(req) != 401 {
			t.Fatal("credentialless login admitted")
		}
		if testutil.GetResponseCookie(req, "whm_access") != "" || testutil.GetResponseCookie(req, "whm_refresh") != "" {
			t.Fatal("login issued token")
		}
		req = testutil.NewJSONRequest(t, map[string]any{"email": p.Email, "password": password, "full_name": "claim attempt", "organization_id": p.OrganizationID})
		if app.Register(req) != nil || testutil.GetResponseStatusCode(req) != 409 {
			t.Fatal("credentialless existing-user registration admitted")
		}
	}
}

func assertRestrictedProcessing(t *testing.T, owner *sql.DB, p Plan) {
	t.Helper()
	u, e := url.Parse(os.Getenv("WORKER_BOOTSTRAP_TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	database, _, worker, _ := p.names()
	u.Path = "/" + database
	u.User = url.UserPassword(worker, fixturePassword)
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var allowed bool
	if e = db.QueryRow(`SELECT public.er_processing_authorize_v1($1,$2,$3,$4::text[])`, p.OrganizationID, p.WorkerID, p.Profile, p.Audiences).Scan(&allowed); e != nil || !allowed {
		t.Fatalf("restricted actual function failed: %v", e)
	}
	if e = db.QueryRow(`SELECT public.er_processing_authorize_v1($1,$2,$3,ARRAY['foreign'])`, p.OrganizationID, p.WorkerID, p.Profile).Scan(&allowed); e != nil || allowed {
		t.Fatal("foreign audience admitted")
	}
	var generation int64
	if e = db.QueryRow(`SELECT generation FROM public.whatomate_schema_seal`).Scan(&generation); e != nil || generation != p.Generation {
		t.Fatal("metadata grant ineffective")
	}
	if _, e = db.Exec(`UPDATE public.users SET is_super_admin=true WHERE id=$1`, p.WorkerID); e == nil {
		t.Fatal("processing user DML admitted")
	}
	if _, e = db.Exec(`DELETE FROM public.er_processing_authorization_bindings`); e == nil {
		t.Fatal("processing binding mutation admitted")
	}
}
