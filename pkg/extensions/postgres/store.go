// Package postgres implements namespaced extension storage. Migrations are
// explicit; constructing a Store does not change a database or core tables.
package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"regexp"
	"time"
)

// ErrCommitUnknown requires reconciliation by the stable idempotency key.
// Callers must not report success or create another message on this outcome.
var ErrCommitUnknown = errors.New("extension transaction commit outcome unknown")

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

var moduleID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func validScope(s ext.Scope) bool {
	return s.OrganizationID != "" && len(s.OrganizationID) <= 256 && moduleID.MatchString(s.ModuleID)
}

const schema = `
CREATE TABLE IF NOT EXISTS extension_records (
 organization_id text NOT NULL, module_id text NOT NULL, key text NOT NULL,
 version bigint NOT NULL CHECK(version>0), data jsonb NOT NULL,
 PRIMARY KEY(organization_id,module_id,key));
CREATE TABLE IF NOT EXISTS extension_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 organization_id text NOT NULL, module_id text NOT NULL,
 action text NOT NULL, resource_id text NOT NULL, actor_id text NOT NULL,
 reason_code text NOT NULL, occurred_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS extension_jobs (
 organization_id text NOT NULL, module_id text NOT NULL, id text NOT NULL,
 kind text NOT NULL, resource_id text NOT NULL, payload jsonb NOT NULL,
 not_before timestamptz NOT NULL, status text NOT NULL DEFAULT 'queued'
 CHECK(status IN ('queued','leased','sent','failed','unknown','held')),
 worker_id text, lease_token text, lease_until timestamptz,
 PRIMARY KEY(organization_id,module_id,id));
CREATE INDEX IF NOT EXISTS extension_jobs_due ON extension_jobs
 (organization_id,module_id,not_before) WHERE status='queued';`

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(760201001)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS extension_schema (id text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(schema)))
	var old string
	err = tx.QueryRowContext(ctx, "SELECT checksum FROM extension_schema WHERE id='storage-v1'").Scan(&old)
	if err == nil {
		if old != hash {
			return fmt.Errorf("extension migration checksum mismatch")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO extension_schema(id,checksum) VALUES ('storage-v1',$1)", hash); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Transact(ctx context.Context, scope ext.Scope, fn func(ext.Tx) error) error {
	if !validScope(scope) || fn == nil {
		return ext.ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(&transaction{tx: tx, scope: scope}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("%w: %w", ErrCommitUnknown, err)
	}
	return nil
}

type transaction struct {
	tx    *sql.Tx
	scope ext.Scope
}

func (t *transaction) Get(ctx context.Context, key string) (ext.Record, error) {
	r := ext.Record{Key: key}
	err := t.tx.QueryRowContext(ctx, `SELECT version,data FROM extension_records WHERE organization_id=$1 AND module_id=$2 AND key=$3`, t.scope.OrganizationID, t.scope.ModuleID, key).Scan(&r.Version, &r.Data)
	return r, err
}
func (t *transaction) Put(ctx context.Context, r ext.Record, expected uint64) error {
	if r.Key == "" || len(r.Key) > 256 || len(r.Data) > 1024*1024 || !json.Valid(r.Data) || expected >= 1<<63-1 {
		return fmt.Errorf("invalid extension record")
	}
	var result sql.Result
	var err error
	if expected == 0 {
		result, err = t.tx.ExecContext(ctx, `INSERT INTO extension_records(organization_id,module_id,key,version,data) VALUES($1,$2,$3,1,$4) ON CONFLICT DO NOTHING`, t.scope.OrganizationID, t.scope.ModuleID, r.Key, []byte(r.Data))
	} else {
		result, err = t.tx.ExecContext(ctx, `UPDATE extension_records SET version=version+1,data=$4 WHERE organization_id=$1 AND module_id=$2 AND key=$3 AND version=$5`, t.scope.OrganizationID, t.scope.ModuleID, r.Key, []byte(r.Data), expected)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ext.ErrConflict
	}
	return nil
}
func (t *transaction) Delete(ctx context.Context, key string, expected uint64) error {
	if expected == 0 || expected >= 1<<63 {
		return ext.ErrConflict
	}
	r, err := t.tx.ExecContext(ctx, `DELETE FROM extension_records WHERE organization_id=$1 AND module_id=$2 AND key=$3 AND version=$4`, t.scope.OrganizationID, t.scope.ModuleID, key, expected)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ext.ErrConflict
	}
	return nil
}
func (t *transaction) Audit(ctx context.Context, a ext.AuditEvent) error {
	if a.Action == "" || a.ResourceID == "" || a.ActorID == "" || a.At.IsZero() || len(a.ReasonCode) > 128 {
		return fmt.Errorf("invalid audit event")
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO extension_audit(organization_id,module_id,action,resource_id,actor_id,reason_code,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, t.scope.OrganizationID, t.scope.ModuleID, a.Action, a.ResourceID, a.ActorID, a.ReasonCode, a.At)
	return err
}
func (t *transaction) Enqueue(ctx context.Context, j ext.Job) error {
	if j.ID == "" || len(j.ID) > 256 || j.Kind == "" || j.ResourceID == "" || j.NotBefore.IsZero() || len(j.Payload) > 8192 || !json.Valid(j.Payload) {
		return fmt.Errorf("invalid job")
	}
	r, err := t.tx.ExecContext(ctx, `INSERT INTO extension_jobs(organization_id,module_id,id,kind,resource_id,payload,not_before) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, t.scope.OrganizationID, t.scope.ModuleID, j.ID, j.Kind, j.ResourceID, []byte(j.Payload), j.NotBefore)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ext.ErrConflict
	}
	return nil
}

type Lease struct {
	ext.Job
	LeaseToken string
	LeaseUntil time.Time
}

// Claim never automatically resubmits a job whose previous worker may have sent
// it. Expired leases become held for explicit reconciliation.
func (s *Store) Claim(ctx context.Context, sc ext.Scope, worker string, now time.Time, duration time.Duration) (Lease, error) {
	if !validScope(sc) || worker == "" || duration <= 0 || duration > time.Hour {
		return Lease{}, ext.ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE extension_jobs SET status='held',worker_id=NULL,lease_token=NULL,lease_until=NULL WHERE organization_id=$1 AND module_id=$2 AND status='leased' AND lease_until<=$3`, sc.OrganizationID, sc.ModuleID, now); err != nil {
		return Lease{}, err
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return Lease{}, err
	}
	l := Lease{LeaseToken: hex.EncodeToString(random[:]), LeaseUntil: now.Add(duration)}
	err = tx.QueryRowContext(ctx, `UPDATE extension_jobs SET status='leased',worker_id=$3,lease_token=$4,lease_until=$5 WHERE (organization_id,module_id,id) IN (SELECT organization_id,module_id,id FROM extension_jobs WHERE organization_id=$1 AND module_id=$2 AND status='queued' AND not_before<=$6 ORDER BY not_before,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,kind,resource_id,payload,not_before`, sc.OrganizationID, sc.ModuleID, worker, l.LeaseToken, l.LeaseUntil, now).Scan(&l.ID, &l.Kind, &l.ResourceID, &l.Payload, &l.NotBefore)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Lease{}, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return Lease{}, fmt.Errorf("%w: %w", ErrCommitUnknown, commitErr)
	}
	return l, err
}
func (s *Store) Complete(ctx context.Context, sc ext.Scope, id, worker, token, status string, now time.Time) error {
	if !validScope(sc) || id == "" || worker == "" || token == "" {
		return ext.ErrDenied
	}
	switch status {
	case "sent", "failed", "unknown", "held":
	default:
		return ext.ErrDenied
	}
	r, err := s.db.ExecContext(ctx, `UPDATE extension_jobs SET status=$6,worker_id=NULL,lease_token=NULL,lease_until=NULL WHERE organization_id=$1 AND module_id=$2 AND id=$3 AND worker_id=$4 AND lease_token=$5 AND status='leased' AND lease_until>$7`, sc.OrganizationID, sc.ModuleID, id, worker, token, status, now)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ext.ErrConflict
	}
	return nil
}
func (s *Store) HoldAfterRestore(ctx context.Context, sc ext.Scope) error {
	if !validScope(sc) {
		return ext.ErrDenied
	}
	_, err := s.db.ExecContext(ctx, `UPDATE extension_jobs SET status='held',worker_id=NULL,lease_token=NULL,lease_until=NULL WHERE organization_id=$1 AND module_id=$2 AND status IN ('queued','leased')`, sc.OrganizationID, sc.ModuleID)
	return err
}
