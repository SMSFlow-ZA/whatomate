//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

func ownerFixture(t *testing.T) (*sql.DB, *sql.DB, configuration) {
	t.Helper()
	// These pins describe synthetic isolated fixtures only, never a production
	// release. Full schema tests below compute their actual disposable catalog.
	release, shape := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256 = release, shape })
	protectedschema.ReleaseIdentity = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	protectedschema.ExpectedCatalogSHA256 = strings.Repeat("c", 64)
	dsn := os.Getenv("EXTENSION_PROCESSING_TEST_DSN")
	if dsn == "" {
		t.Skip("explicit disposable PostgreSQL fixture required")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	c := validConfiguration()
	c.InstanceID = uuid.NewString()
	suffix := strings.ReplaceAll(c.InstanceID, "-", "")
	c.InstanceRoot = "/srv/whatomate/er/" + suffix
	c.DatabaseName = "er_" + suffix
	c.DatabaseRole = "er_processing_" + suffix
	c.DatabaseDSNFile = c.InstanceRoot + "/config/processing.dsn"
	c.Supervisor.JobRoot = c.InstanceRoot + "/jobs"
	c.Supervisor.DefinitionsRoot = c.InstanceRoot + "/definitions"
	if _, e = admin.Exec("CREATE DATABASE " + c.DatabaseName); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("CREATE ROLE " + c.DatabaseRole + " LOGIN PASSWORD 'synthetic-processing-role-only' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + c.DatabaseName
	params := u.Query()
	params.Set("search_path", "public")
	params.Set("application_name", c.DatabaseName)
	u.RawQuery = params.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	u.User = url.UserPassword(c.DatabaseRole, "synthetic-processing-role-only")
	worker, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	worker.SetMaxOpenConns(5)
	t.Cleanup(func() {
		worker.Close()
		db.Close()
		admin.Exec("DROP DATABASE " + c.DatabaseName)
		admin.Exec("DROP ROLE IF EXISTS er_processing_owner_" + suffix)
		admin.Exec("DROP ROLE " + c.DatabaseRole)
		admin.Close()
	})
	_, e = db.Exec(`CREATE TABLE organizations(id uuid PRIMARY KEY,deleted_at timestamptz);
 CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid,role_id uuid,is_active boolean,is_super_admin boolean NOT NULL DEFAULT false,password_hash text,deleted_at timestamptz);
 CREATE TABLE custom_roles(id uuid PRIMARY KEY,organization_id uuid,is_system boolean,is_default boolean,deleted_at timestamptz);
 CREATE TABLE permissions(id uuid PRIMARY KEY,resource text,action text,deleted_at timestamptz);
 CREATE TABLE role_permissions(custom_role_id uuid,permission_id uuid);
 CREATE TABLE whatomate_protection_state(id smallint PRIMARY KEY,protected boolean NOT NULL);
 INSERT INTO whatomate_protection_state VALUES(1,true);`)
	if e != nil {
		t.Fatal(e)
	}
	role, p := uuid.NewString(), uuid.NewString()
	for _, x := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations(id)VALUES($1)`, []any{c.OrganizationID}},
		{`INSERT INTO users(id,organization_id,role_id,is_active)VALUES($1,$2,$3,true)`, []any{c.WorkerUserID, c.OrganizationID, role}},
		{`INSERT INTO custom_roles VALUES($1,$2,false,false,NULL)`, []any{role, c.OrganizationID}},
		{`INSERT INTO permissions VALUES($1,'knowledge','job',NULL)`, []any{p}},
		{`INSERT INTO role_permissions VALUES($1,$2)`, []any{role, p}},
	} {
		if _, e := db.Exec(x.query, x.args...); e != nil {
			t.Fatal(e)
		}
	}
	store := api.AccessStore{DB: db}
	ctx := context.Background()
	if e := store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := store.ReplaceVersioned(ctx, api.AccessReplacement{OrganizationID: c.OrganizationID, UserID: c.WorkerUserID, OperatorID: c.WorkerUserID, Action: "replace", Reason: "synthetic worker fixture", Grants: []api.Grant{{Permission: "knowledge:job", Audience: "staff"}}, Metadata: json.RawMessage(`{"kind":"knowledge-worker","profile":"reviewed-worker-v1"}`)}, true, func(context.Context, *sql.Tx, bool) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if e := knowledge.MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}

	migration, e := authorizationMigration(c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(migration); e != nil {
		t.Fatal(e)
	}
	var definition string
	if e = db.QueryRow(`SELECT pg_catalog.pg_get_functiondef(pg_catalog.to_regprocedure($1))`, authorizationSignature).Scan(&definition); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte(definition))
	c.AuthorizationFunctionSHA256 = hex.EncodeToString(digest[:])
	if _, e = db.Exec("GRANT USAGE ON SCHEMA public TO " + c.DatabaseRole + "; GRANT SELECT,UPDATE ON addon_knowledge_jobs TO " + c.DatabaseRole); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`CREATE TABLE messages(id integer,content text);CREATE TABLE contacts(id integer);CREATE TABLE conversations(id integer);CREATE TABLE whatsapp_accounts(id integer);`); e != nil {
		t.Fatal(e)
	}
	return db, worker, c
}
func TestCurrentWorkerProofRejectsRevocationHumanProfileAndForeignAudience(t *testing.T) {
	db, worker, c := ownerFixture(t)
	ctx := context.Background()
	auth := ownerAuthorization(worker, c)
	check := func(j knowledge.ProcessingJob) error {
		tx, e := worker.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		return auth(ctx, tx, j)
	}
	job := knowledge.ProcessingJob{TenantID: c.OrganizationID, Metadata: knowledge.UploadMetadata{Audiences: []string{"staff"}}}
	if e := check(job); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`UPDATE users SET is_super_admin=true WHERE id=$1`, c.WorkerUserID); e != nil {
		t.Fatal(e)
	}
	if check(job) == nil {
		t.Fatal("superadmin promotion retained worker authority")
	}
	if _, e := db.Exec(`UPDATE users SET is_super_admin=false WHERE id=$1`, c.WorkerUserID); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`UPDATE whatomate_protection_state SET protected=false`); e != nil {
		t.Fatal(e)
	}
	if check(job) == nil {
		t.Fatal("unprotected database accepted")
	}
	if _, e := db.Exec(`UPDATE whatomate_protection_state SET protected=true`); e != nil {
		t.Fatal(e)
	}
	foreign := job
	foreign.TenantID = uuid.NewString()
	if check(foreign) == nil {
		t.Fatal("foreign owner accepted")
	}
	foreign = job
	foreign.Metadata.Audiences = []string{"management"}
	if check(foreign) == nil {
		t.Fatal("ungranted audience accepted")
	}
	if _, e := db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, c.WorkerUserID); e != nil {
		t.Fatal(e)
	}
	if check(job) == nil {
		t.Fatal("revoked membership accepted")
	}
	if _, e := db.Exec(`UPDATE users SET is_active=true WHERE id=$1`, c.WorkerUserID); e != nil {
		t.Fatal(e)
	}
	if e := (api.AccessStore{DB: db}).Replace(ctx, c.OrganizationID, c.WorkerUserID, []api.Grant{{Permission: "knowledge:job", Audience: "staff"}}); e != nil {
		t.Fatal(e)
	}
	if check(job) == nil {
		t.Fatal("unaudited legacy/human principal became worker")
	}
}

func TestCompletionSerializesWithCurrentWorkerRevocation(t *testing.T) {
	db, worker, c := ownerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := []byte("%PDF-synthetic-owner-boundary")
	meta := knowledge.UploadMetadata{DocumentID: "doc", Version: "v1", Filename: "a.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Now().UTC()}
	mb, _ := json.Marshal(meta)
	hash := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	if _, e := db.Exec(`INSERT INTO addon_knowledge_jobs(id,tenant,actor,input,input_hash,metadata,metadata_hash)VALUES('job',$1,$2,$3,$4,$5,$6)`, c.OrganizationID, c.WorkerUserID, input, hash(input), mb, hash(mb)); e != nil {
		t.Fatal(e)
	}
	auth := ownerAuthorization(worker, c)
	queue := knowledge.NewAuthorizedProcessingQueue(worker, c.OrganizationID, auth)
	j, e := queue.Claim(ctx, c.WorkerUserID, 8*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	// Revoke commits before artifact commit; callback observes it in the same tx
	// that would store the artifact. Lease remains processing/unknown, never retry.
	if e := (api.AccessStore{DB: db}).Replace(ctx, c.OrganizationID, c.WorkerUserID, nil); e != nil {
		t.Fatal(e)
	}
	receipt := knowledge.ProcessingReceipt{JobID: j.ID, InputHash: j.InputHash, MetadataHash: j.MetadataHash, OutputHash: strings.Repeat("a", 64), ProfileID: c.ProfileID, ImageDigest: "sha256:" + strings.Repeat("b", 64), DefinitionsRevision: "current"}
	if e := queue.CompleteWithReceipt(ctx, j, []knowledge.Page{{Number: 1, Text: "fixture", Method: "text", ExtractorRevision: "fixture"}}, knowledge.Evidence{ContentHash: j.InputHash, MalwareClean: true, ExtractionComplete: true, ScannerRevision: "fixture"}, receipt); e == nil {
		t.Fatal("revoked worker committed artifact")
	}
	var status string
	var output sql.NullString
	if e := db.QueryRow(`SELECT status,document::text FROM addon_knowledge_jobs WHERE id='job'`).Scan(&status, &output); e != nil || status != "processing" || output.Valid {
		t.Fatalf("artifact after revoke: %s %v %v", status, output, e)
	}
}

func TestCompletionHoldsOwnerGrantLockUntilArtifactCommit(t *testing.T) {
	db, worker, c := ownerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	input := []byte("%PDF-synthetic-concurrent-owner-boundary")
	meta := knowledge.UploadMetadata{DocumentID: "doc", Version: "v1", Filename: "a.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Now().UTC()}
	mb, _ := json.Marshal(meta)
	hash := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	if _, e := db.Exec(`INSERT INTO addon_knowledge_jobs(id,tenant,actor,input,input_hash,metadata,metadata_hash)VALUES('job',$1,$2,$3,$4,$5,$6)`, c.OrganizationID, c.WorkerUserID, input, hash(input), mb, hash(mb)); e != nil {
		t.Fatal(e)
	}
	auth := ownerAuthorization(worker, c)
	q := knowledge.NewAuthorizedProcessingQueue(worker, c.OrganizationID, auth)
	j, e := q.Claim(ctx, c.WorkerUserID, 8*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	q = knowledge.NewAuthorizedProcessingQueue(worker, c.OrganizationID, func(ctx context.Context, tx *sql.Tx, j knowledge.ProcessingJob) error {
		if e := auth(ctx, tx, j); e != nil {
			return e
		}
		close(locked)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	receipt := knowledge.ProcessingReceipt{JobID: j.ID, InputHash: j.InputHash, MetadataHash: j.MetadataHash, OutputHash: strings.Repeat("a", 64), ProfileID: c.ProfileID, ImageDigest: "sha256:" + strings.Repeat("b", 64), DefinitionsRevision: "current"}
	complete := make(chan error, 1)
	go func() {
		complete <- q.CompleteWithReceipt(ctx, j, []knowledge.Page{{Number: 1, Text: "fixture", Method: "text", ExtractorRevision: "fixture"}}, knowledge.Evidence{ContentHash: j.InputHash, MalwareClean: true, ExtractionComplete: true, ScannerRevision: "fixture"}, receipt)
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("completion did not lock current owner")
	}
	revoke := make(chan error, 1)
	go func() { revoke <- (api.AccessStore{DB: db}).Replace(ctx, c.OrganizationID, c.WorkerUserID, nil) }()
	blocked := false
	for i := 0; i < 200; i++ {
		if e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%pg_advisory_xact_lock%')`).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case e := <-revoke:
			t.Fatalf("revocation crossed artifact transaction: %v", e)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("no genuine revocation lock wait observed")
	}
	close(release)
	if e := <-complete; e != nil {
		t.Fatalf("authorized pre-revocation completion: %v", e)
	}
	if e := <-revoke; e != nil {
		t.Fatal(e)
	}
	var status string
	if e := db.QueryRow(`SELECT status FROM addon_knowledge_jobs WHERE id='job'`).Scan(&status); e != nil || status != "processed" {
		t.Fatalf("artifact transaction not committed %s %v", status, e)
	}
}

func TestRestrictedWorkerCannotLockOrMutateHostAndColumnLeakIsDenied(t *testing.T) {
	db, worker, c := ownerFixture(t)
	ctx := context.Background()
	if e := databaseAuthorization(ctx, worker, c); e != nil {
		t.Fatalf("restricted nonowner startup: %v", e)
	}
	if _, e := worker.Exec(`UPDATE public.users SET is_active=false`); e == nil {
		t.Fatal("worker has core UPDATE")
	}
	if _, e := worker.Exec(`SELECT id FROM public.users FOR SHARE`); e == nil {
		t.Fatal("worker has direct host-row lock rights")
	}
	if _, e := db.Exec("GRANT SELECT(content) ON public.messages TO " + c.DatabaseRole); e != nil {
		t.Fatal(e)
	}
	if e := databaseAuthorization(ctx, worker, c); e == nil {
		t.Fatal("column-only transcript SELECT bypassed restricted role gate")
	}
}

func TestOwnerFunctionDefinitionOwnerAndACLHaveNoFallback(t *testing.T) {
	db, worker, c := ownerFixture(t)
	ctx := context.Background()
	check := func(c configuration) error {
		tx, e := worker.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		return ownerAuthorization(worker, c)(ctx, tx, knowledge.ProcessingJob{TenantID: c.OrganizationID, Metadata: knowledge.UploadMetadata{Audiences: []string{"staff"}}})
	}
	if e := check(c); e != nil {
		t.Fatal(e)
	}
	bad := c
	bad.AuthorizationFunctionSHA256 = strings.Repeat("0", 64)
	if check(bad) == nil {
		t.Fatal("unreviewed function accepted")
	}
	if _, e := db.Exec("GRANT EXECUTE ON FUNCTION " + authorizationSignature + " TO PUBLIC"); e != nil {
		t.Fatal(e)
	}
	if check(c) == nil {
		t.Fatal("PUBLIC function execution accepted")
	}
	if _, e := db.Exec("REVOKE ALL ON FUNCTION " + authorizationSignature + " FROM PUBLIC"); e != nil {
		t.Fatal(e)
	}
	owner := "er_processing_owner_" + strings.ReplaceAll(c.InstanceID, "-", "")
	if _, e := db.Exec("ALTER ROLE " + owner + " LOGIN"); e != nil {
		t.Fatal(e)
	}
	if check(c) == nil {
		t.Fatal("login owner accepted")
	}
	if _, e := db.Exec("ALTER ROLE " + owner + " NOLOGIN"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec("ALTER FUNCTION " + authorizationSignature + " SET search_path=public,pg_catalog"); e != nil {
		t.Fatal(e)
	}
	if check(c) == nil {
		t.Fatal("unsafe function search path accepted")
	}
}
