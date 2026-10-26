package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/jackc/pgx/v5/pgconn"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresLockedDownWorkerQueue(t *testing.T) {
	owner := disposable(t)
	p := prepareFixture(t, owner)
	database, _, worker, _ := p.names()
	if _, e := owner.Exec("REVOKE CONNECT ON DATABASE " + database + " FROM PUBLIC"); e != nil {
		t.Fatal(e)
	}
	refreshPins(t, owner, &p)
	s := fixtureSession(t, owner, p)
	if r := s.Execute(context.Background(), p, "apply"); r.Outcome != "committed-observed" {
		t.Fatalf("apply %s %s", r.Outcome, r.Code)
	}
	u, e := url.Parse(os.Getenv("WORKER_BOOTSTRAP_TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	u.User = url.UserPassword(worker, fixturePassword)
	u.Path = "/" + database
	tls := os.Getenv("WORKER_BOOTSTRAP_TEST_TLS")
	if tls == "" {
		t.Fatal("explicit disposable TLS fixture required")
	}
	u.Host = "localhost:5432"
	v := u.Query()
	v.Set("sslmode", "verify-full")
	v.Set("sslrootcert", tls+"/server.crt")
	u.RawQuery = v.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.Ping(); e != nil {
		t.Fatalf("real restricted TLS login failed: %v", e)
	}
	authorize := func(ctx context.Context, tx *sql.Tx, j knowledge.ProcessingJob) error {
		if e := (api.AccessStore{DB: db}).LockVersionedTx(ctx, tx, p.OrganizationID, p.WorkerID); e != nil {
			return e
		}
		var allowed bool
		e := tx.QueryRowContext(ctx, `SELECT public.er_processing_authorize_v1($1,$2,$3,$4::text[])`, p.OrganizationID, p.WorkerID, p.Profile, j.Metadata.Audiences).Scan(&allowed)
		if e != nil {
			return e
		}
		if !allowed {
			return denied
		}
		return nil
	}
	metadata := knowledge.UploadMetadata{DocumentID: "SYNTHETIC-QUEUE-ONLY", Version: "1", Filename: "synthetic.pdf", MediaType: "application/pdf", Audiences: p.Audiences, Effective: time.Now().UTC()}
	id := "synthetic-worker-job"
	input := []byte("%PDF-synthetic-queue-only")
	rawMetadata, _ := json.Marshal(metadata)
	if _, e = owner.Exec(`INSERT INTO public.addon_knowledge_jobs(id,tenant,actor,input,input_hash,metadata,metadata_hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, p.OrganizationID, p.OperatorID, input, digest(input), rawMetadata, digest(rawMetadata)); e != nil {
		t.Fatal(e)
	}
	q := knowledge.NewAuthorizedProcessingQueue(db, p.OrganizationID, authorize)
	job, e := q.Claim(context.Background(), p.WorkerID, 8*time.Minute)
	if e != nil {
		t.Fatalf("real restricted claim failed: %v", e)
	}
	if job.ID != id {
		t.Fatal("wrong claim")
	}
	receipt := knowledge.ProcessingReceipt{JobID: id, InputHash: job.InputHash, MetadataHash: job.MetadataHash, OutputHash: strings.Repeat("a", 64), ProfileID: p.Profile, ImageDigest: "sha256:" + strings.Repeat("b", 64), DefinitionsRevision: "synthetic-fixture"}
	// Synthetic queue adapter evidence only; no scanner/publication success claim.
	evidence := knowledge.Evidence{ContentHash: job.InputHash, ScannerRevision: "synthetic-queue-adapter", MalwareClean: true, ExtractionComplete: true}
	if e = q.CompleteWithReceipt(context.Background(), job, []knowledge.Page{{Number: 1, Text: "synthetic", Method: "text", ExtractorRevision: "synthetic-adapter"}}, evidence, receipt); e != nil {
		t.Fatalf("real restricted complete failed: %v", e)
	}
	var status string
	var version int
	if e = owner.QueryRow("SELECT status,version FROM public.addon_knowledge_jobs WHERE id=$1", id).Scan(&status, &version); e != nil || status != "processed" || version != 3 {
		t.Fatal("queue completion not committed")
	}
	// Failure is terminal and uses the same restricted column grants.
	if _, e = owner.Exec(`INSERT INTO public.addon_knowledge_jobs(id,tenant,actor,input,input_hash,metadata,metadata_hash) VALUES('synthetic-worker-fail',$1,$2,$3,$4,$5,$6)`, p.OrganizationID, p.OperatorID, input, digest(input), rawMetadata, digest(rawMetadata)); e != nil {
		t.Fatal(e)
	}
	failed, e := q.Claim(context.Background(), p.WorkerID, 8*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = q.Fail(context.Background(), failed, "never persist raw adapter error", false); e != nil {
		t.Fatal(e)
	}
	var failure string
	if e = owner.QueryRow("SELECT status,version,failure FROM public.addon_knowledge_jobs WHERE id=$1", failed.ID).Scan(&status, &version, &failure); e != nil || status != "failed" || version != 3 || failure != "processing_failed" {
		t.Fatal("failed job was not terminal/sanitized")
	}
	if _, e = q.Claim(context.Background(), p.WorkerID, 8*time.Minute); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("failed job replayed")
	}
	for _, statement := range []string{"DELETE FROM public.addon_knowledge_jobs", "INSERT INTO public.addon_knowledge_jobs(id) VALUES('forbidden')", "UPDATE public.addon_knowledge_jobs SET approval='{}'::jsonb", "SELECT password_hash FROM public.users", "SELECT * FROM public.addon_knowledge_publications", "INSERT INTO public.addon_knowledge_publications(tenant) VALUES('forbidden')"} {
		_, e = db.Exec(statement)
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || pg.Code != "42501" {
			t.Fatalf("expected actual permission denial, got %v", e)
		}
	}
}
