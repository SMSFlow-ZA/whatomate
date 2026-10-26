package main

import (
	"context"
	"database/sql"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"os"
	"testing"
)

func TestStoreNeverReplaysUnknownCommit(t *testing.T) {
	p := fixturePlan()
	s := Session{}
	r := s.failedResult(p, errCommitUnknown)
	if r.Outcome != "unknown-held" || r.Code != "commit_unknown" {
		t.Fatalf("unsafe result: %+v", r)
	}
}
func TestSQLContractContainsNoFunctionDDLOrCredential(t *testing.T) {
	for _, s := range provisioningSQL(fixturePlan()) {
		for _, forbidden := range []string{"CREATE OR REPLACE", "PASSWORD", "LOGIN SUPERUSER", "GRANT ALL"} {
			if containsFold(s, forbidden) {
				t.Fatal("unsafe statement")
			}
		}
	}
}
func disposable(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("WORKER_BOOTSTRAP_TEST_DSN")
	if dsn == "" {
		t.Skip("WORKER_BOOTSTRAP_TEST_DSN must name an explicitly disposable owner database")
	}
	if os.Getenv("WORKER_BOOTSTRAP_TEST_DISPOSABLE") != "yes" {
		t.Fatal("explicit disposable opt-in required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal("disposable connection unavailable")
	}
	return newFixtureDatabase(t, db, dsn)
}
func TestPostgresAtomicRollback(t *testing.T) {
	db := disposable(t)
	p := prepareFixture(t, db)
	s := fixtureSession(t, db, p)
	// A real constraint fails AFTER identity, role and metadata ACL writes. The
	// AccessStore callback and access replacement must share one transaction.
	if _, e := db.Exec(`ALTER TABLE public.er_processing_authorization_bindings ADD CONSTRAINT reject_fixture CHECK(false) NOT VALID`); e != nil {
		t.Fatal(e)
	}
	refreshPins(t, db, &p)
	r := s.Execute(context.Background(), p, "apply")
	if r.Outcome != "refused" {
		t.Fatalf("expected rollback, got %s", r.Outcome)
	}
	assertAbsent(t, db, p)
}
func TestPostgresApplyAndReadOnlyReconcile(t *testing.T) {
	db := disposable(t)
	p := prepareFixture(t, db)
	s := fixtureSession(t, db, p)
	r := s.Execute(context.Background(), p, "apply")
	if r.Outcome != "committed-observed" {
		_, stateErr := s.state(context.Background(), p)
		t.Logf("synthetic state query: %v", stateErr)
		t.Fatalf("apply: %s %s; schema=%v function=%v committed=%v", r.Outcome, r.Code, s.schema(context.Background(), p), functionState(context.Background(), db, p, true), s.committed(context.Background(), p))
	}
	if r = s.Execute(context.Background(), p, "reconcile"); r.Outcome != "committed-observed" {
		t.Fatalf("reconcile: %s %s", r.Outcome, r.Code)
	}
	assertCredentiallessAuthentication(t, db, p)
	// The fixed SECURITY DEFINER function must authorize only this credentialless
	// profile/audience. Exercise it as the real restricted LOGIN, never SET ROLE.
	assertRestrictedProcessing(t, db, p)
	// Repeating apply refuses; it cannot append a second audit or adopt the row.
	if r = s.Execute(context.Background(), p, "apply"); r.Outcome == "committed-observed" {
		t.Fatal("apply replay accepted")
	}
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM addon_api_access_audit WHERE user_id=$1`, p.WorkerID).Scan(&count); e != nil || count != 1 {
		t.Fatal("audit duplicated")
	}
}

func TestPostgresLeaseAndQuotaAreNotBypassed(t *testing.T) {
	db := disposable(t)
	p := prepareFixture(t, db)
	s := fixtureSession(t, db, p)
	s.PID++
	if r := s.Execute(context.Background(), p, "apply"); r.Outcome != "refused" {
		t.Fatal("foreign backend accepted")
	}
	assertAbsent(t, db, p)
	s.PID--
	if _, e := db.Exec(`SELECT pg_advisory_unlock_all()`); e != nil {
		t.Fatal(e)
	}
	if r := s.Execute(context.Background(), p, "apply"); r.Outcome != "refused" {
		t.Fatal("missing lease accepted")
	}
	assertAbsent(t, db, p)
	var held bool
	if e := db.QueryRow(`SELECT pg_try_advisory_lock($1)`, protectedschema.LockID).Scan(&held); e != nil || !held {
		t.Fatal("lease reacquisition failed")
	}
	if _, e := db.Exec(`CREATE OR REPLACE FUNCTION public.fixture_quota() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture quota full'; END $$`); e != nil {
		t.Fatal(e)
	}
	refreshPins(t, db, &p)
	if r := s.Execute(context.Background(), p, "apply"); r.Outcome != "refused" {
		t.Fatal("existing user quota bypassed")
	}
	assertAbsent(t, db, p)
}

func TestPostgresInspectReturnsActualPinsAndReconcileTamperingHeld(t *testing.T) {
	db := disposable(t)
	p := prepareFixture(t, db)
	s := fixtureSession(t, db, p)
	r := s.Execute(context.Background(), p, "inspect")
	if r.Outcome != "observed" || r.CatalogSHA256 != p.CatalogSHA256 || r.FunctionSHA256 != p.FunctionSHA256 {
		t.Fatal("inspect omitted actual validated pins")
	}
	r = s.Execute(context.Background(), p, "apply")
	if r.Outcome != "committed-observed" {
		t.Fatal(r.Outcome)
	}
	if _, e := db.Exec(`UPDATE public.users SET sso_binding='unexpected' WHERE id=$1`, p.WorkerID); e != nil {
		t.Fatal(e)
	}
	if r = s.Execute(context.Background(), p, "reconcile"); r.Outcome != "unresolved-held" {
		t.Fatal("tampered committed data accepted")
	}
}
