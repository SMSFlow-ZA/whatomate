package protectedschema

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func schemaDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	vectorDSN := os.Getenv("SCHEMA_VECTOR_TEST_DSN")
	if vectorDSN != "" {
		dsn = vectorDSN
	}
	if dsn == "" {
		t.Skip("real TEST_DATABASE_URL required")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "seal_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec("CREATE SCHEMA " + schema); e != nil {
		t.Fatal(e)
	}
	pool, e := sql.Open("pgx", dsn+"&search_path="+schema)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	return pool, schema
}

func TestOfflineBaselineSealAndDMLOnlyRuntime(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	vectorDSN := os.Getenv("SCHEMA_VECTOR_TEST_DSN")
	if vectorDSN != "" {
		dsn = vectorDSN
	}
	if dsn == "" {
		t.Skip("real local test PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	plain := e == nil && u.Hostname() == "db" && u.User.Username() == "addon_test" && u.Path == "/hosttests"
	nativeVector := e == nil && vectorDSN != "" && u.Hostname() == "er-release-vector-20261022" && u.User.Username() == "er_vector_test" && u.Path == "/er_vector"
	if !plain && !nativeVector {
		t.Fatal("refuse nonlocal/shared-target test DSN")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	names := []string{}
	pools := []*sql.DB{}
	role := "schema_rt_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	t.Cleanup(func() {
		for _, pool := range pools {
			pool.Close()
		}
		for _, name := range names {
			if !regexp.MustCompile(`^schema_contract_test_[a-f0-9]{24}$`).MatchString(name) {
				t.Errorf("unsafe cleanup name")
				continue
			}
			// Cancellation tests can leave PostgreSQL finishing a driver cancel
			// handshake after sql.DB.Close. FORCE applies only to this freshly
			// created, exact-prefix DB on the validated disposable local server.
			if _, e := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)"); e != nil {
				t.Errorf("drop own test DB: %v", e)
			}
		}
		if _, e := admin.Exec("DROP ROLE IF EXISTS " + role); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	fresh := func() (*gorm.DB, *sql.DB, *config.Config) {
		name := "schema_contract_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
		if _, e := admin.Exec("CREATE DATABASE " + name); e != nil {
			t.Fatal(e)
		}
		names = append(names, name)
		local := *u
		local.Path = "/" + name
		owner, e := gorm.Open(postgres.Open(local.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if e != nil {
			t.Fatal(e)
		}
		pool, e := owner.DB()
		if e != nil {
			t.Fatal(e)
		}
		pool.SetMaxOpenConns(5)
		pools = append(pools, pool)
		if nativeVector {
			if _, e = pool.Exec("CREATE EXTENSION vector"); e != nil {
				t.Fatal(e)
			}
		}
		c := &config.Config{}
		c.Extensions.ProtectedMode = true
		if nativeVector {
			c.Extensions.KnowledgeVectorEnabled = true
			c.Extensions.KnowledgeVectorIdentity = "synthetic-reviewed-embedding-generation-v1"
			c.Extensions.AIEmbeddingDimensions = 1536
		}
		c.Database.SchemaManagedExternally = true
		c.Database.Name = name
		c.Database.User = u.User.Username()
		c.Database.SchemaRelease = ReleaseIdentity
		c.DefaultAdmin = config.DefaultAdminConfig{Email: "operator@example.invalid", Password: "synthetic-schema-owner-password", FullName: "Schema test owner"}
		return owner, pool, c
	}
	oldR, oldS := ReleaseIdentity, ExpectedCatalogSHA256
	t.Cleanup(func() { ReleaseIdentity = oldR; ExpectedCatalogSHA256 = oldS })
	ReleaseIdentity = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	ExpectedCatalogSHA256 = ""
	reference, _, referenceConfig := fresh()
	ctx := context.Background()
	baseline, e := Install(ctx, reference, referenceConfig, "", true)
	if e != nil {
		t.Fatal(e)
	}
	ExpectedCatalogSHA256 = baseline.SHA256
	owner, pool, c := fresh()
	c.Database.SchemaSHA256 = ExpectedCatalogSHA256
	if e = Migrate(ctx, owner, c); e != nil {
		t.Fatal(e)
	}
	// Actual per-deployment processing bindings are owner data. They must not
	// make the fixed source catalog differ from its empty, deny-all baseline.
	if _, e = pool.Exec("INSERT INTO public.er_processing_authorization_bindings VALUES(current_database(),'synthetic-unbound-worker',$1,$2,'synthetic-profile',ARRAY['staff'])", uuid.NewString(), uuid.NewString()); e != nil {
		t.Fatal(e)
	}
	var org models.Organization
	if e = owner.First(&org).Error; e != nil {
		t.Fatal(e)
	}
	actual, e := Install(ctx, owner, c, org.ID.String(), false)
	if e != nil {
		observed, _ := Catalog(ctx, pool, "public")
		expected := map[string]bool{}
		for _, s := range baseline.Items {
			expected[s] = true
		}
		for _, s := range observed.Items {
			if !expected[s] {
				t.Log("unexpected canonical item", s)
			}
		}
		t.Fatalf("seal differs: baseline=%s actual=%s: %v", baseline.SHA256, observed.SHA256, e)
	}
	if actual.SHA256 != baseline.SHA256 {
		t.Fatal("baseline/target mismatch")
	}
	if _, e = Install(ctx, owner, c, org.ID.String(), false); e == nil {
		t.Fatal("repeated seal accepted")
	}
	password := strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec("CREATE ROLE " + role + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + password + "'"); e != nil {
		t.Fatal(e)
	}
	for _, statement := range []string{"REVOKE ALL ON DATABASE " + c.Database.Name + " FROM PUBLIC", "GRANT CONNECT ON DATABASE " + c.Database.Name + " TO " + role} {
		if _, e = admin.Exec(statement); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = pool.Exec("REVOKE ALL ON SCHEMA public FROM PUBLIC;GRANT USAGE ON SCHEMA public TO " + role + ";GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + role + ";GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO " + role); e != nil {
		t.Fatal(e)
	}
	forbidden := map[string]bool{"whatomate_schema_seal": true, "whatomate_schema_attempt": true, "extensions_bootstrap_attempt": true, "whatomate_protection_state": true, "extension_security_schema": true, "extension_schema": true, "addon_cases_schema": true, "addon_knowledge_vector_profiles": true, "addon_knowledge_vector_workers": true, "er_processing_authorization_bindings": true}
	rows, e := pool.Query("SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN('r','p')")
	if e != nil {
		t.Fatal(e)
	}
	tables := []string{}
	for rows.Next() {
		var name string
		rows.Scan(&name)
		if !forbidden[name] {
			tables = append(tables, name)
		}
	}
	rows.Close()
	for _, table := range tables {
		if _, e = pool.Exec("GRANT INSERT,UPDATE,DELETE ON " + table + " TO " + role); e != nil {
			t.Fatal(e)
		}
	}
	runtimeURL := *u
	runtimeURL.Path = "/" + c.Database.Name
	runtimeURL.User = url.UserPassword(role, password)
	runtimeConfig := *c
	runtimeConfig.Database = c.Database
	runtimeConfig.Database.User = role
	runtimeConfig.Database.Host = u.Hostname()
	runtimeConfig.Database.Port, _ = strconv.Atoi(u.Port())
	runtimeConfig.Database.Password = password
	runtimeConfig.Database.SSLMode = "disable"
	guarded, e := OpenRuntime(&runtimeConfig)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e := guarded.DB()
	if e != nil {
		t.Fatal(e)
	}
	pools = append(pools, runtime)
	lease, e := Acquire(ctx, runtime, &runtimeConfig)
	if e != nil {
		t.Fatal("DML-only runtime refused", e)
	}
	var exclusive bool
	if e = pool.QueryRow("SELECT pg_try_advisory_lock($1)", LockID).Scan(&exclusive); e != nil || exclusive {
		t.Fatalf("runtime lease did not exclude installer: %v %v", exclusive, e)
	}
	var leasePID int
	if e = lease.conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&leasePID); e != nil {
		t.Fatal(e)
	}
	sibling, e := runtime.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sibling.ExecContext(ctx, "SELECT 1"); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec("SELECT pg_terminate_backend($1)", leasePID); e != nil {
		t.Fatal(e)
	}
	if lease.Check(ctx) == nil {
		t.Fatal("lost primary session remained authorized")
	}
	// A normal business transaction on the surviving pool connection remains
	// fenced, even while the dedicated monitor is about to stop the process.
	if _, e = sibling.ExecContext(ctx, "UPDATE addon_cases_case SET version=version WHERE false"); e != nil {
		t.Fatal("ordinary fenced business query failed", e)
	}
	if e = pool.QueryRow("SELECT pg_try_advisory_lock($1)", LockID).Scan(&exclusive); e != nil || exclusive {
		t.Fatal("surviving runtime pool connection did not fence installer", e)
	}
	lease.Close()
	sibling.Close()
	runtime.Close()
	ownerLease, e := pool.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = ownerLease.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", LockID).Scan(&exclusive); e != nil || !exclusive {
		t.Fatal("runtime pool did not release locks on physical close", e)
	}
	if unsafe, e := OpenRuntime(&runtimeConfig); e == nil {
		p, _ := unsafe.DB()
		p.Close()
		t.Fatal("reconnect admitted while owner exclusive lease held")
	}
	ownerLease.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", LockID)
	ownerLease.Close()
	guarded, e = OpenRuntime(&runtimeConfig)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e = guarded.DB()
	if e != nil {
		t.Fatal(e)
	}
	pools = append(pools, runtime)
	if _, e = runtime.Exec("ALTER TABLE addon_cases_case ADD COLUMN forbidden text"); e == nil {
		t.Fatal("runtime altered schema")
	}
	if _, e = runtime.Exec("UPDATE whatomate_schema_seal SET catalog_sha256='tampered'"); e == nil {
		t.Fatal("runtime edited seal")
	}
	if _, e = runtime.Exec("UPDATE whatomate_protection_state SET protected=false"); e == nil {
		t.Fatal("runtime edited marker")
	}
	if _, e = pool.Exec("ALTER TABLE addon_cases_case ADD COLUMN unexpected text"); e != nil {
		t.Fatal(e)
	}
	if Verify(ctx, runtime, &runtimeConfig) == nil {
		t.Fatal("catalog tamper accepted")
	}
	if _, e = pool.Exec("ALTER TABLE addon_cases_case DROP COLUMN unexpected"); e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, runtime, &runtimeConfig); e != nil {
		t.Fatal(e)
	}
	identity := runtimeConfig.Database.User
	runtimeConfig.Database.User = "different_session_identity"
	if Verify(ctx, runtime, &runtimeConfig) == nil {
		t.Fatal("effective/session identity mismatch accepted")
	}
	runtimeConfig.Database.User = identity
	if _, e = pool.Exec("GRANT EXECUTE ON FUNCTION pg_catalog.lo_import(text) TO " + role); e != nil {
		t.Fatal(e)
	}
	if Verify(ctx, runtime, &runtimeConfig) == nil {
		t.Fatal("server-file import authority accepted")
	}
	if _, e = pool.Exec("REVOKE EXECUTE ON FUNCTION pg_catalog.lo_import(text) FROM " + role); e != nil {
		t.Fatal(e)
	}
	// A code-only release can have the same SQL catalog. Historical seals must
	// never authorize its older binary after an explicit newer installation.
	releaseA := ReleaseIdentity
	if e = runtime.Close(); e != nil {
		t.Fatal(e)
	}
	ReleaseIdentity = strings.Repeat("c", 40) + ":" + strings.Repeat("d", 40)
	c.Database.SchemaRelease = ReleaseIdentity
	if _, e = Install(ctx, owner, c, org.ID.String(), false); e != nil {
		t.Fatal(e)
	}
	runtimeConfig.Database.SchemaRelease = ReleaseIdentity
	guarded, e = OpenRuntime(&runtimeConfig)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e = guarded.DB()
	if e != nil {
		t.Fatal(e)
	}
	pools = append(pools, runtime)
	if e = Verify(ctx, runtime, &runtimeConfig); e != nil {
		t.Fatal("current B refused", e)
	}
	ReleaseIdentity = releaseA
	runtimeConfig.Database.SchemaRelease = releaseA
	if Verify(ctx, runtime, &runtimeConfig) == nil {
		t.Fatal("historical A accepted after B")
	}
	ReleaseIdentity = c.Database.SchemaRelease
	runtimeConfig.Database.SchemaRelease = ReleaseIdentity
	// Simulate the durable state after an owner attempt lost its physical lock.
	if _, e = pool.Exec("INSERT INTO whatomate_schema_attempt(release,catalog_sha256) VALUES($1,$2)", strings.Repeat("e", 40)+":"+strings.Repeat("f", 40), ExpectedCatalogSHA256); e != nil {
		t.Fatal(e)
	}
	if Verify(ctx, runtime, &runtimeConfig) == nil {
		t.Fatal("runtime accepted incomplete owner attempt")
	}
	if _, e = pool.Exec("DELETE FROM whatomate_schema_attempt WHERE release=$1", strings.Repeat("e", 40)+":"+strings.Repeat("f", 40)); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec("GRANT CREATE ON SCHEMA public TO " + role); e != nil {
		t.Fatal(e)
	}
	if Verify(ctx, runtime, &runtimeConfig) == nil {
		t.Fatal("DDL privilege escalation accepted")
	}
	// Kill the actual owner's physical lock session after its attempt commits.
	// A blocked owner API cannot complete/seal; cancellation must leave durable
	// evidence that rejects both reconnecting runtimes and a different installer.
	runtime.Close()
	blocker, e := pool.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, "SELECT pg_advisory_lock(19631022)"); e != nil {
		t.Fatal(e)
	}
	defer func() {
		blocker.ExecContext(context.Background(), "SELECT pg_advisory_unlock(19631022)")
		blocker.Close()
	}()
	releaseB := ReleaseIdentity
	releaseC := strings.Repeat("e", 40) + ":" + strings.Repeat("a", 40)
	ReleaseIdentity = releaseC
	c.Database.SchemaRelease = releaseC
	migrationCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Install(migrationCtx, owner, c, org.ID.String(), false); done <- err }()
	var ownerPID int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e = pool.QueryRow(`SELECT pid FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted AND classid=($1::bigint >> 32)::oid AND objid=($1::bigint & 4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND EXISTS(SELECT 1 FROM whatomate_schema_attempt WHERE release=$2)`, LockID, releaseC).Scan(&ownerPID)
		if e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ownerPID == 0 {
		cancel()
		<-done
		t.Fatal("owner never reached durable-attempt boundary", e)
	}
	if _, e = pool.Exec("SELECT pg_terminate_backend($1)", ownerPID); e != nil {
		t.Fatal(e)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost owner session sealed")
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("owner API was not canceled on lease loss")
	}
	var seals int
	if e = pool.QueryRow("SELECT count(*) FROM whatomate_schema_seal WHERE release=$1", releaseC).Scan(&seals); e != nil || seals != 0 {
		t.Fatal("lost owner attempt wrote seal", e)
	}
	ReleaseIdentity = releaseB
	runtimeConfig.Database.SchemaRelease = releaseB
	if unsafe, e := OpenRuntime(&runtimeConfig); e == nil {
		p, _ := unsafe.DB()
		p.Close()
		t.Fatal("pending failed owner admitted runtime")
	}
	ReleaseIdentity = strings.Repeat("f", 40) + ":" + strings.Repeat("a", 40)
	c.Database.SchemaRelease = ReleaseIdentity
	if _, e = Install(ctx, owner, c, org.ID.String(), false); e == nil {
		t.Fatal("another installer bypassed incomplete attempt")
	}
}
func TestCompiledIdentityCannotBeConfigForged(t *testing.T) {
	oldR, oldS := ReleaseIdentity, ExpectedCatalogSHA256
	t.Cleanup(func() { ReleaseIdentity = oldR; ExpectedCatalogSHA256 = oldS })
	ReleaseIdentity = ""
	ExpectedCatalogSHA256 = ""
	c := &config.Config{}
	c.Extensions.ProtectedMode = true
	c.Database.SchemaManagedExternally = true
	c.Database.SchemaRelease = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	c.Database.SchemaSHA256 = strings.Repeat("c", 64)
	if Validate(c) == nil {
		t.Fatal("config invented compiled identity")
	}
	ReleaseIdentity = c.Database.SchemaRelease
	ExpectedCatalogSHA256 = c.Database.SchemaSHA256
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
	c.Database.SchemaSHA256 = strings.Repeat("d", 64)
	if Validate(c) == nil {
		t.Fatal("config overrode compiled shape")
	}
}
func TestCatalogBindsColumnsConstraintsTriggersFunctionsAndNotRows(t *testing.T) {
	db, s := schemaDB(t)
	ctx := context.Background()
	if _, e := db.Exec(`CREATE TABLE sample(id bigint PRIMARY KEY,value text NOT NULL);CREATE FUNCTION watched() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW;END$$;CREATE TRIGGER watched BEFORE INSERT ON sample FOR EACH ROW EXECUTE FUNCTION watched()`); e != nil {
		t.Fatal(e)
	}
	original, e := Catalog(ctx, db, s)
	if e != nil {
		t.Fatal(e)
	}
	db.Exec("INSERT INTO sample VALUES(1,'private row never in catalog')")
	after, e := Catalog(ctx, db, s)
	if e != nil || after.SHA256 != original.SHA256 {
		t.Fatalf("row-dependent hash: %v", e)
	}
	for _, stmt := range []string{"ALTER TABLE sample ALTER COLUMN value DROP NOT NULL", "ALTER TABLE sample ADD CONSTRAINT value_nonempty CHECK(value<>'')", "ALTER TABLE sample DISABLE TRIGGER watched", "CREATE OR REPLACE FUNCTION watched() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NULL;END$$"} {
		if _, e := db.Exec(stmt); e != nil {
			t.Fatal(e)
		}
		next, e := Catalog(ctx, db, s)
		if e != nil || next.SHA256 == after.SHA256 {
			t.Fatalf("tamper not hashed: %s %v", stmt, e)
		}
		after = next
	}
}
