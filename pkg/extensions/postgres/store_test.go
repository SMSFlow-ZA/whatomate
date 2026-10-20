package postgres

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/jackc/pgx/v5/stdlib"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("EXTENSION_DATABASE_URL")
	if dsn == "" {
		t.Skip("EXTENSION_DATABASE_URL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db)
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func uniqueScope(t *testing.T) ext.Scope {
	return ext.Scope{OrganizationID: t.Name() + time.Now().Format("150405.000000000"), ModuleID: "cases"}
}

func TestTransactionRollsBackRecordAuditAndAcknowledgement(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sc := uniqueScope(t)
	failure := errors.New("intake failed")
	err := s.Transact(ctx, sc, func(tx ext.Tx) error {
		if err := tx.Put(ctx, ext.Record{Key: "case-1", Data: []byte(`{"status":"submitted"}`)}, 0); err != nil {
			return err
		}
		if err := tx.Audit(ctx, ext.AuditEvent{Action: "submitted", ResourceID: "case-1", ActorID: "a", At: time.Now()}); err != nil {
			return err
		}
		if err := tx.Enqueue(ctx, ext.Job{ID: "ack-1", Kind: "acknowledgement", ResourceID: "case-1", Payload: []byte(`{}`), NotBefore: time.Now()}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var n int
	for _, table := range []string{"extension_records", "extension_audit", "extension_jobs"} {
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE organization_id=$1 AND module_id=$2", sc.OrganizationID, sc.ModuleID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s leaked after rollback: %d %v", table, n, err)
		}
	}
}

func TestScopedCASAllowsExactlyOneConcurrentWriter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sc := uniqueScope(t)
	if err := s.Transact(ctx, sc, func(tx ext.Tx) error { return tx.Put(ctx, ext.Record{Key: "case-1", Data: []byte(`{}`)}, 0) }); err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Transact(ctx, sc, func(tx ext.Tx) error {
				return tx.Put(ctx, ext.Record{Key: "case-1", Data: []byte(`{"updated":true}`)}, 1)
			})
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ext.ErrConflict) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if winners.Load() != 1 {
		t.Fatalf("%d concurrent CAS winners", winners.Load())
	}
	other := sc
	other.OrganizationID += "other"
	if err := s.Transact(ctx, other, func(tx ext.Tx) error { _, err := tx.Get(ctx, "case-1"); return err }); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-org read: %v", err)
	}
	other = sc
	other.ModuleID = "directory"
	if err := s.Transact(ctx, other, func(tx ext.Tx) error { _, err := tx.Get(ctx, "case-1"); return err }); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-module read: %v", err)
	}
}

func TestJobLeaseCannotBeClaimedTwiceAndExpiredLeaseIsHeld(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sc := uniqueScope(t)
	now := time.Now().UTC()
	if err := s.Transact(ctx, sc, func(tx ext.Tx) error {
		return tx.Enqueue(ctx, ext.Job{ID: "one", Kind: "ack", ResourceID: "case", Payload: []byte(`{}`), NotBefore: now.Add(-time.Minute)})
	}); err != nil {
		t.Fatal(err)
	}
	j, err := s.Claim(ctx, sc, "worker-a", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, sc, "worker-b", now, time.Second); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("leased twice: %v", err)
	}
	if _, err = s.Claim(ctx, sc, "worker-b", now.Add(2*time.Second), time.Second); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired job must be held: %v", err)
	}
	if err = s.Complete(ctx, sc, j.ID, "worker-a", j.LeaseToken, "sent", now.Add(2*time.Second)); err == nil {
		t.Fatal("late completion accepted")
	}
	var status string
	if err = s.db.QueryRowContext(ctx, "SELECT status FROM extension_jobs WHERE organization_id=$1 AND module_id=$2 AND id='one'", sc.OrganizationID, sc.ModuleID).Scan(&status); err != nil || status != "held" {
		t.Fatalf("status %s %v", status, err)
	}
}

func TestRestoreHoldsAllUndispatchedJobs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sc := uniqueScope(t)
	if err := s.Transact(ctx, sc, func(tx ext.Tx) error {
		return tx.Enqueue(ctx, ext.Job{ID: "one", Kind: "ack", ResourceID: "case", Payload: []byte(`{}`), NotBefore: time.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldAfterRestore(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, sc, "worker", time.Now(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("restored job dispatched: %v", err)
	}
}
