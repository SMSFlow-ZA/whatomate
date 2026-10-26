package protectedschema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
)

// CompatibleRequest identifies immutable, already installed source history.
// The successor release and catalog come only from this compiled artifact.
type CompatibleRequest struct {
	PreviousRelease    string
	PreviousGeneration int64
	OrganizationID     string
}

type CompatibleResult struct {
	Release            string `json:"release"`
	CatalogSHA256      string `json:"catalog_sha256"`
	PreviousRelease    string `json:"previous_release"`
	PreviousGeneration int64  `json:"previous_generation"`
	Generation         int64  `json:"generation"`
	Sealed             bool   `json:"sealed"`
	RuntimeStarted     bool   `json:"runtime_started"`
	DispatchHeld       bool   `json:"dispatch_held"`
	DDLExecuted        bool   `json:"ddl_executed"`
	MigrationsExecuted bool   `json:"migrations_executed"`
}

func ValidateCompatible(c *config.Config, r CompatibleRequest) error {
	org, err := uuid.Parse(r.OrganizationID)
	if Validate(c) != nil || err != nil || org == uuid.Nil || org.String() != r.OrganizationID ||
		!releasePattern.MatchString(r.PreviousRelease) || r.PreviousRelease == ReleaseIdentity ||
		r.PreviousGeneration <= 0 || r.PreviousGeneration == math.MaxInt64 ||
		c.App.Environment != "production" || c.App.Debug || c.Plan.Enabled ||
		c.Extensions.CarrierEnabled || c.Extensions.CarrierDispatchEnabled || !c.Channels.StopDispatch ||
		!namespacePattern.MatchString(c.Database.Name) || !namespacePattern.MatchString(c.Database.User) ||
		c.DefaultAdmin.Email == "" || strings.TrimSpace(c.DefaultAdmin.Email) != c.DefaultAdmin.Email {
		return ErrDenied
	}
	return nil
}

// Existing installations can retain their bootstrap superuser. It must still
// be the exact configured database owner and session identity; this path never
// changes role flags, memberships, ACLs, or privileges.
func compatibleOwner(ctx context.Context, conn *sql.Conn, c *config.Config) error {
	var name, user, session, namespace string
	var owner, authorized, extra bool
	err := conn.QueryRowContext(ctx, `SELECT current_database(),current_user,session_user,current_schema(),
 EXISTS(SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND d.datdba=(SELECT oid FROM pg_roles WHERE rolname=current_user)),
 EXISTS(SELECT 1 FROM pg_roles WHERE rolname=current_user AND rolcanlogin AND (rolsuper OR (NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls))),
 EXISTS(SELECT 1 FROM pg_namespace WHERE left(nspname,3)<>'pg_' AND nspname NOT IN('public','information_schema'))`).Scan(&name, &user, &session, &namespace, &owner, &authorized, &extra)
	if err != nil || name != c.Database.Name || user != c.Database.User || session != user || namespace != "public" || !owner || !authorized || extra {
		return ErrDenied
	}
	return nil
}

func compatibleLease(ctx context.Context, conn *sql.Conn, pid int) error {
	var held bool
	err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()=$1 AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND mode='ExclusiveLock' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=($2::bigint>>32)::oid AND objid=($2::bigint&4294967295)::oid)`, pid, LockID).Scan(&held)
	if err != nil || !held {
		return ErrDenied
	}
	return nil
}

func compatibleState(ctx context.Context, tx *sql.Tx, c *config.Config, r CompatibleRequest, ownAttempt, readonly bool) error {
	var generation int64
	var release, shape string
	sealSQL := `SELECT generation,release,catalog_sha256 FROM public.whatomate_schema_seal ORDER BY generation DESC LIMIT 1`
	if !readonly {
		sealSQL += ` FOR SHARE`
	}
	if err := tx.QueryRowContext(ctx, sealSQL).Scan(&generation, &release, &shape); err != nil || generation != r.PreviousGeneration || release != r.PreviousRelease || shape != ExpectedCatalogSHA256 {
		return ErrDenied
	}
	// Apply holds these authenticated rows through commit. A serializable
	// snapshot alone can legally serialize before concurrent deactivation.
	if !readonly {
		var id int
		var org, administratorID string
		if e := tx.QueryRowContext(ctx, `SELECT id FROM public.whatomate_protection_state WHERE id=1 AND protected FOR SHARE`).Scan(&id); e != nil {
			return ErrDenied
		}
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM public.organizations WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE`, r.OrganizationID).Scan(&org); e != nil {
			return ErrDenied
		}
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM public.users WHERE email=$1 AND organization_id=$2::uuid AND is_active AND deleted_at IS NULL FOR SHARE`, c.DefaultAdmin.Email, r.OrganizationID).Scan(&administratorID); e != nil {
			return ErrDenied
		}
	}
	var protected, organization, administrator, pending bool
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*)=1 AND coalesce(bool_and(id=1 AND protected),false) FROM public.whatomate_protection_state),
 (SELECT count(*)=1 AND coalesce(bool_and(id=$1::uuid AND deleted_at IS NULL),false) FROM public.organizations),
 (SELECT count(*)=1 FROM public.users WHERE email=$2 AND organization_id=$1::uuid AND is_active AND deleted_at IS NULL),
 EXISTS(SELECT 1 FROM public.whatomate_schema_attempt a WHERE a.catalog_sha256<>'baseline'
 AND NOT EXISTS(SELECT 1 FROM public.whatomate_schema_seal s WHERE s.release=a.release AND s.catalog_sha256=a.catalog_sha256)
 AND NOT($3 AND a.release=$4 AND a.catalog_sha256=$5))`, r.OrganizationID, c.DefaultAdmin.Email, ownAttempt, ReleaseIdentity, ExpectedCatalogSHA256).Scan(&protected, &organization, &administrator, &pending)
	if err != nil || !protected || !organization || !administrator || pending {
		return ErrDenied
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM public.whatomate_schema_attempt WHERE release=$1 AND catalog_sha256=$2`, ReleaseIdentity, ExpectedCatalogSHA256).Scan(&attempts); err != nil || attempts != map[bool]int{false: 0, true: 1}[ownAttempt] {
		return ErrDenied
	}
	return nil
}

func compatibleTransaction(ctx context.Context, conn *sql.Conn, c *config.Config, r CompatibleRequest, ownAttempt, readonly bool) (*sql.Tx, Manifest, error) {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: readonly})
	if err != nil {
		return nil, Manifest{}, err
	}
	fail := func(err error) (*sql.Tx, Manifest, error) { tx.Rollback(); return nil, Manifest{}, err }
	if _, err = tx.ExecContext(ctx, `SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='25s'`); err != nil {
		return fail(err)
	}
	if err = compatibleState(ctx, tx, c, r, ownAttempt, readonly); err != nil {
		return fail(err)
	}
	manifest, err := capture(ctx, tx, "public")
	if err != nil {
		return fail(err)
	}
	if manifest.SHA256 != ExpectedCatalogSHA256 || manifest.Release != ReleaseIdentity {
		return fail(ErrDenied)
	}
	return tx, manifest, nil
}

// SealCompatible performs no migrations, DDL, grants, role changes or function
// replacement. A physical catalog mismatch refuses even if an old seal matches.
// The durable attempt is committed separately: interruption cannot be retried.
func SealCompatible(ctx context.Context, db *sql.DB, c *config.Config, r CompatibleRequest, apply bool) (manifest Manifest, result CompatibleResult, err error) {
	if db == nil || ValidateCompatible(c, r) != nil {
		return manifest, result, ErrDenied
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return manifest, result, err
	}
	defer conn.Close()
	if err = compatibleOwner(ctx, conn, c); err != nil {
		return manifest, result, err
	}
	var held bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, LockID).Scan(&held); err != nil || !held {
		return manifest, result, ErrDenied
	}
	defer func() {
		done, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if e := conn.QueryRowContext(done, `SELECT pg_advisory_unlock($1)`, LockID).Scan(&unlocked); e != nil || !unlocked {
			conn.Raw(func(any) error { return driver.ErrBadConn })
			if err == nil {
				err = ErrCommitUnknown
			}
		}
	}()
	var pid int
	if err = conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		return manifest, result, err
	}
	if err = compatibleLease(ctx, conn, pid); err != nil {
		return manifest, result, err
	}
	tx, manifest, err := compatibleTransaction(ctx, conn, c, r, false, !apply)
	if err != nil {
		return manifest, result, err
	}
	defer tx.Rollback()
	result = CompatibleResult{Release: ReleaseIdentity, CatalogSHA256: ExpectedCatalogSHA256, PreviousRelease: r.PreviousRelease, PreviousGeneration: r.PreviousGeneration, Generation: r.PreviousGeneration + 1, DispatchHeld: true}
	if !apply {
		err = tx.Commit()
		return manifest, result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO public.whatomate_schema_attempt(release,catalog_sha256) VALUES($1,$2)`, ReleaseIdentity, ExpectedCatalogSHA256); err != nil {
		return manifest, result, err
	}
	if err = tx.Commit(); err != nil {
		return manifest, result, errors.Join(ErrCommitUnknown, err)
	}
	// From this point every failure means keep isolated and inspect the durable
	// attempt. A second invocation never retries it or runs schema migrations.
	if err = compatibleLease(ctx, conn, pid); err != nil {
		return manifest, result, errors.Join(ErrCommitUnknown, err)
	}
	tx, manifest, err = compatibleTransaction(ctx, conn, c, r, true, false)
	if err != nil {
		return manifest, result, errors.Join(ErrCommitUnknown, err)
	}
	defer tx.Rollback()
	var actual int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO public.whatomate_schema_seal(release,catalog_sha256) VALUES($1,$2) RETURNING generation`, ReleaseIdentity, ExpectedCatalogSHA256).Scan(&actual); err != nil || actual != r.PreviousGeneration+1 {
		return manifest, result, errors.Join(ErrCommitUnknown, ErrDenied, err)
	}
	if err = tx.Commit(); err != nil {
		return manifest, result, errors.Join(ErrCommitUnknown, err)
	}
	result.Sealed = true
	return manifest, result, nil
}
