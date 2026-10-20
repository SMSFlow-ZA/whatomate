package security

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"strings"
	"time"
)

const (
	EnrollPermission            = "security:mfa.enroll"
	RevokePermission            = "security:mfa.revoke"
	BreakGlassRequestPermission = "security:breakglass.request"
	BreakGlassApprovePermission = "security:breakglass.approve"
	BreakGlassUsePermission     = "security:breakglass.use"
	BreakGlassRevokePermission  = "security:breakglass.revoke"
)

type Config struct {
	Drift                        int
	EnrollmentTTL, MaxBreakGlass time.Duration
	AllowedBreakGlassPermissions []string
	Clock                        func() time.Time
}
type Service struct {
	db                           *sql.DB
	cipher                       secretCipher
	drift                        int
	enrollmentTTL, maxBreakGlass time.Duration
	allowed                      map[string]bool
	now                          func() time.Time
}

func (s *Service) String() string   { return "security.Service([REDACTED])" }
func (s *Service) GoString() string { return s.String() }
func New(db *sql.DB, key []byte, cfg Config) (*Service, error) {
	if db == nil || cfg.Drift < 0 || cfg.Drift > 1 || cfg.EnrollmentTTL <= 0 || cfg.EnrollmentTTL > time.Hour || cfg.MaxBreakGlass <= 0 {
		return nil, ErrInvalid
	}
	cipher, e := newSecretCipher(key)
	if e != nil {
		return nil, e
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	allowed := map[string]bool{}
	for _, permission := range cfg.AllowedBreakGlassPermissions {
		if permission == "" || !strings.Contains(permission, ":") {
			return nil, ErrInvalid
		}
		allowed[permission] = true
	}
	return &Service{db: db, cipher: cipher, drift: cfg.Drift, enrollmentTTL: cfg.EnrollmentTTL, maxBreakGlass: cfg.MaxBreakGlass, allowed: allowed, now: clock}, nil
}

// Verification is a detached read-only proof for the trusted session issuer.
// It must not be accepted as an HTTP request body or reused across generations.
type Verification struct {
	organizationID, userID string
	generation             uint64
	verifiedAt             time.Time
}

func (v Verification) OrganizationID() string { return v.organizationID }
func (v Verification) UserID() string         { return v.userID }
func (v Verification) Generation() uint64     { return v.generation }
func (v Verification) VerifiedAt() time.Time  { return v.verifiedAt }
func (v Verification) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		OrganizationID, UserID string
		Generation             uint64
		VerifiedAt             time.Time
	}{v.organizationID, v.userID, v.generation, v.verifiedAt})
}

type CredentialView struct {
	generation uint64
	enabled    bool
}

func (v CredentialView) Generation() uint64 { return v.generation }
func (v CredentialView) Enabled() bool      { return v.enabled }
func (v CredentialView) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Generation uint64
		Enabled    bool
	}{v.generation, v.enabled})
}

type Enrollment struct {
	id        string
	secret    Secret
	expiresAt time.Time
}

func (v Enrollment) ID() string           { return v.id }
func (v Enrollment) Secret() Secret       { return v.secret }
func (v Enrollment) ExpiresAt() time.Time { return v.expiresAt }
func (v Enrollment) String() string       { return "security.Enrollment([REDACTED])" }
func (v Enrollment) GoString() string     { return v.String() }
func (v Enrollment) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID        string
		ExpiresAt time.Time
		Secret    Secret
	}{v.id, v.expiresAt, v.secret})
}

type Activation struct {
	verification Verification
	recovery     []Secret
}

func (v Activation) Verification() Verification { return v.verification }
func (v Activation) RecoveryCodes() []Secret    { return append([]Secret(nil), v.recovery...) }
func (v Activation) String() string             { return "security.Activation([REDACTED])" }
func (v Activation) GoString() string           { return v.String() }
func (v Activation) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Verification  Verification
		RecoveryCodes []Secret
	}{v.verification, v.recovery})
}

const securitySchema = `
CREATE TABLE IF NOT EXISTS extension_security_mfa (
 organization_id text NOT NULL,user_id text NOT NULL,generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),enabled boolean NOT NULL DEFAULT false,
 secret_cipher bytea NOT NULL DEFAULT ''::bytea,pending_cipher bytea NOT NULL DEFAULT ''::bytea,pending_id text NOT NULL DEFAULT '',pending_expires timestamptz,
 last_counter bigint NOT NULL DEFAULT -1,failed_attempts integer NOT NULL DEFAULT 0,locked_until timestamptz,
 PRIMARY KEY(organization_id,user_id));
CREATE TABLE IF NOT EXISTS extension_security_recovery (
 organization_id text NOT NULL,user_id text NOT NULL,generation bigint NOT NULL,code_hash bytea NOT NULL,used_at timestamptz,
 PRIMARY KEY(organization_id,user_id,generation,code_hash),FOREIGN KEY(organization_id,user_id) REFERENCES extension_security_mfa(organization_id,user_id));
CREATE TABLE IF NOT EXISTS extension_security_breakglass (
 id text PRIMARY KEY,organization_id text NOT NULL,user_id text NOT NULL,resource_id text NOT NULL,permission text NOT NULL,reason text NOT NULL,
 credential_generation bigint NOT NULL,requested_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,approved_by text NOT NULL DEFAULT '',approved_at timestamptz,revoked_at timestamptz,version bigint NOT NULL DEFAULT 1,
 CHECK(expires_at>requested_at),CHECK(approved_by='' OR approved_by<>user_id));
CREATE INDEX IF NOT EXISTS extension_security_breakglass_user_idx ON extension_security_breakglass(organization_id,user_id);
CREATE TABLE IF NOT EXISTS extension_security_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,organization_id text NOT NULL,user_id text NOT NULL,actor_id text NOT NULL,
 action text NOT NULL,resource_id text NOT NULL,reason text NOT NULL,occurred_at timestamptz NOT NULL);
`

func (s *Service) Migrate(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(760201003)`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS extension_security_schema(id text PRIMARY KEY,checksum text NOT NULL)`); e != nil {
		return e
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(securitySchema)))
	var old string
	e = tx.QueryRowContext(ctx, `SELECT checksum FROM extension_security_schema WHERE id='security-v1'`).Scan(&old)
	if e == nil {
		if old != checksum {
			return errors.New("security migration checksum mismatch")
		}
		return tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if _, e = tx.ExecContext(ctx, securitySchema); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO extension_security_schema(id,checksum) VALUES('security-v1',$1)`, checksum); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Service) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return errors.Join(ErrCommitUnknown, e)
	}
	return nil
}
func identity(org, user string) bool {
	return org != "" && user != "" && len(org) <= 256 && len(user) <= 256
}
func actorAllowed(a ext.Actor, permission string) bool {
	return identity(a.OrganizationID, a.UserID) && a.Has(permission)
}
func (s *Service) audit(ctx context.Context, tx *sql.Tx, org, user, actor, action, resource, reason string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO extension_security_audit(organization_id,user_id,actor_id,action,resource_id,reason,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, org, user, actor, action, resource, reason, s.now().UTC())
	return e
}

type credential struct {
	generation                  uint64
	enabled                     bool
	secret, pending             []byte
	pendingID                   string
	pendingExpires, lockedUntil sql.NullTime
	lastCounter                 int64
	failures                    int
}

func loadCredential(ctx context.Context, tx *sql.Tx, org, user string) (credential, error) {
	var c credential
	e := tx.QueryRowContext(ctx, `SELECT generation,enabled,secret_cipher,pending_cipher,pending_id,pending_expires,last_counter,failed_attempts,locked_until FROM extension_security_mfa WHERE organization_id=$1 AND user_id=$2 FOR UPDATE`, org, user).Scan(&c.generation, &c.enabled, &c.secret, &c.pending, &c.pendingID, &c.pendingExpires, &c.lastCounter, &c.failures, &c.lockedUntil)
	return c, e
}
func (s *Service) failed(ctx context.Context, tx *sql.Tx, org, user string, c credential) error {
	failures := c.failures + 1
	var until any
	if failures >= 5 {
		until = s.now().UTC().Add(5 * time.Minute)
	}
	_, e := tx.ExecContext(ctx, `UPDATE extension_security_mfa SET failed_attempts=$1,locked_until=$2 WHERE organization_id=$3 AND user_id=$4`, failures, until, org, user)
	return e
}
func (s *Service) locked(c credential) bool {
	return c.lockedUntil.Valid && s.now().Before(c.lockedUntil.Time)
}
func (s *Service) BeginEnrollment(ctx context.Context, a ext.Actor) (Enrollment, error) {
	if !actorAllowed(a, EnrollPermission) {
		return Enrollment{}, ErrDenied
	}
	token, e := randomToken()
	if e != nil {
		return Enrollment{}, e
	}
	seedToken, e := randomToken()
	if e != nil {
		return Enrollment{}, e
	}
	seed, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(seedToken)
	if e != nil {
		return Enrollment{}, e
	}
	defer clear(seed)
	sealed, e := s.cipher.seal(seed, a.OrganizationID, a.UserID, "enrollment:"+token)
	if e != nil {
		return Enrollment{}, e
	}
	expires := s.now().UTC().Add(s.enrollmentTTL)
	var denied bool
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO extension_security_mfa(organization_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, a.OrganizationID, a.UserID); e != nil {
			return e
		}
		c, e := loadCredential(ctx, tx, a.OrganizationID, a.UserID)
		if e != nil {
			return e
		}
		if s.locked(c) || !enrollmentActorCurrent(c, a) {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.enrollment.denied", a.UserID, "verification_required")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE extension_security_mfa SET pending_cipher=$1,pending_id=$2,pending_expires=$3 WHERE organization_id=$4 AND user_id=$5`, sealed, token, expires, a.OrganizationID, a.UserID); e != nil {
			return e
		}
		return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.enrollment.begin", a.UserID, "self_enrollment")
	})
	if e != nil {
		return Enrollment{}, e
	}
	if denied {
		return Enrollment{}, ErrDenied
	}
	return Enrollment{id: token, secret: newSecret(seedToken), expiresAt: expires}, nil
}
func (s *Service) ConfirmEnrollment(ctx context.Context, a ext.Actor, enrollmentID, code string) (Activation, error) {
	if !actorAllowed(a, EnrollPermission) {
		return Activation{}, ErrDenied
	}
	var result Activation
	var denied bool
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := loadCredential(ctx, tx, a.OrganizationID, a.UserID)
		if errors.Is(e, sql.ErrNoRows) {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.enrollment.denied", a.UserID, "invalid_enrollment")
		}
		if e != nil {
			return e
		}
		if c.pendingID != enrollmentID || !c.pendingExpires.Valid || !s.now().Before(c.pendingExpires.Time) || s.locked(c) || !enrollmentActorCurrent(c, a) {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.enrollment.denied", a.UserID, "invalid_enrollment")
		}
		seed, e := s.cipher.open(c.pending, a.OrganizationID, a.UserID, "enrollment:"+enrollmentID)
		if e != nil {
			return e
		}
		defer clear(seed)
		counter, ok := matchCounter(seed, code, s.now(), s.drift, -1)
		if !ok {
			denied = true
			if e = s.failed(ctx, tx, a.OrganizationID, a.UserID, c); e != nil {
				return e
			}
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.enrollment.denied", a.UserID, "invalid_code")
		}
		next := c.generation + 1
		sealed, e := s.cipher.seal(seed, a.OrganizationID, a.UserID, activePurpose(next))
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE extension_security_mfa SET generation=$1,enabled=true,secret_cipher=$2,pending_cipher=''::bytea,pending_id='',pending_expires=NULL,last_counter=$3,failed_attempts=0,locked_until=NULL WHERE organization_id=$4 AND user_id=$5 AND generation=$6`, next, sealed, counter, a.OrganizationID, a.UserID, c.generation); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM extension_security_recovery WHERE organization_id=$1 AND user_id=$2`, a.OrganizationID, a.UserID); e != nil {
			return e
		}
		codes := make([]Secret, 0, 10)
		for range 10 {
			raw, e := randomToken()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO extension_security_recovery(organization_id,user_id,generation,code_hash) VALUES($1,$2,$3,$4)`, a.OrganizationID, a.UserID, next, recoveryHash(a.OrganizationID, a.UserID, next, raw)); e != nil {
				return e
			}
			codes = append(codes, newSecret(raw))
		}
		result = Activation{verification: Verification{organizationID: a.OrganizationID, userID: a.UserID, generation: next, verifiedAt: s.now().UTC()}, recovery: codes}
		return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.enrollment.confirm", a.UserID, "activated")
	})
	if e != nil {
		return Activation{}, e
	}
	if denied {
		return Activation{}, ErrDenied
	}
	return result, nil
}

// VerifyTOTP and VerifyRecovery are for the trusted password-authenticated login
// bridge. Neither method establishes primary identity or issues a session.
func (s *Service) VerifyTOTP(ctx context.Context, org, user, code string) (Verification, error) {
	return s.verify(ctx, org, user, code, false)
}
func (s *Service) VerifyRecovery(ctx context.Context, org, user, code string) (Verification, error) {
	return s.verify(ctx, org, user, code, true)
}
func (s *Service) verify(ctx context.Context, org, user, code string, recovery bool) (Verification, error) {
	if !identity(org, user) {
		return Verification{}, ErrDenied
	}
	var proof Verification
	var denied bool
	action := "mfa.totp"
	if recovery {
		action = "mfa.recovery"
	}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := loadCredential(ctx, tx, org, user)
		if errors.Is(e, sql.ErrNoRows) {
			denied = true
			return s.audit(ctx, tx, org, user, user, action+".denied", user, "not_enabled")
		}
		if e != nil {
			return e
		}
		if !c.enabled || s.locked(c) {
			denied = true
			return s.audit(ctx, tx, org, user, user, action+".denied", user, "not_enabled_or_locked")
		}
		valid := false
		if recovery {
			if len(code) == 32 {
				result, e := tx.ExecContext(ctx, `UPDATE extension_security_recovery SET used_at=$1 WHERE organization_id=$2 AND user_id=$3 AND generation=$4 AND code_hash=$5 AND used_at IS NULL`, s.now().UTC(), org, user, c.generation, recoveryHash(org, user, c.generation, code))
				if e != nil {
					return e
				}
				n, e := result.RowsAffected()
				if e != nil {
					return e
				}
				valid = n == 1
			}
		} else {
			seed, e := s.cipher.open(c.secret, org, user, activePurpose(c.generation))
			if e != nil {
				return e
			}
			defer clear(seed)
			counter, ok := matchCounter(seed, code, s.now(), s.drift, c.lastCounter)
			if ok {
				result, e := tx.ExecContext(ctx, `UPDATE extension_security_mfa SET last_counter=$1 WHERE organization_id=$2 AND user_id=$3 AND generation=$4 AND enabled AND last_counter=$5`, counter, org, user, c.generation, c.lastCounter)
				if e != nil {
					return e
				}
				n, e := result.RowsAffected()
				if e != nil {
					return e
				}
				valid = n == 1
			}
		}
		if !valid {
			denied = true
			if e = s.failed(ctx, tx, org, user, c); e != nil {
				return e
			}
			return s.audit(ctx, tx, org, user, user, action+".denied", user, "invalid_or_replayed")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE extension_security_mfa SET failed_attempts=0,locked_until=NULL WHERE organization_id=$1 AND user_id=$2`, org, user); e != nil {
			return e
		}
		proof = Verification{organizationID: org, userID: user, generation: c.generation, verifiedAt: s.now().UTC()}
		return s.audit(ctx, tx, org, user, user, action+".verified", user, "verified")
	})
	if e != nil {
		return Verification{}, e
	}
	if denied {
		return Verification{}, ErrDenied
	}
	return proof, nil
}
func (s *Service) Generation(ctx context.Context, org, user string) (CredentialView, error) {
	if !identity(org, user) {
		return CredentialView{}, ErrDenied
	}
	var v CredentialView
	e := s.db.QueryRowContext(ctx, `SELECT generation,enabled FROM extension_security_mfa WHERE organization_id=$1 AND user_id=$2`, org, user).Scan(&v.generation, &v.enabled)
	if errors.Is(e, sql.ErrNoRows) {
		return CredentialView{}, nil
	}
	return v, e
}
func (s *Service) CheckGeneration(ctx context.Context, org, user string, generation uint64) error {
	current, e := s.Generation(ctx, org, user)
	if e != nil {
		return e
	}
	if current.generation != generation {
		return ErrDenied
	}
	return nil
}
func (s *Service) Revoke(ctx context.Context, a ext.Actor, expected uint64) (CredentialView, error) {
	if !actorAllowed(a, RevokePermission) || !a.MFA {
		return CredentialView{}, ErrDenied
	}
	var result CredentialView
	var denied bool
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := loadCredential(ctx, tx, a.OrganizationID, a.UserID)
		if errors.Is(e, sql.ErrNoRows) {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.revoke.denied", a.UserID, "not_enabled")
		}
		if e != nil {
			return e
		}
		if !c.enabled || c.generation != expected || c.generation != a.MFAGeneration {
			denied = true
			return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.revoke.denied", a.UserID, "stale_generation")
		}
		next := c.generation + 1
		if _, e = tx.ExecContext(ctx, `UPDATE extension_security_mfa SET generation=$1,enabled=false,secret_cipher=''::bytea,pending_cipher=''::bytea,pending_id='',pending_expires=NULL,last_counter=-1,failed_attempts=0,locked_until=NULL WHERE organization_id=$2 AND user_id=$3 AND generation=$4`, next, a.OrganizationID, a.UserID, c.generation); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM extension_security_recovery WHERE organization_id=$1 AND user_id=$2`, a.OrganizationID, a.UserID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE extension_security_breakglass SET revoked_at=$1,version=version+1 WHERE organization_id=$2 AND user_id=$3 AND revoked_at IS NULL`, s.now().UTC(), a.OrganizationID, a.UserID); e != nil {
			return e
		}
		result = CredentialView{generation: next}
		return s.audit(ctx, tx, a.OrganizationID, a.UserID, a.UserID, "mfa.revoke", a.UserID, "credential_generation_revoked")
	})
	if e != nil {
		return CredentialView{}, e
	}
	if denied {
		return CredentialView{}, ErrDenied
	}
	return result, nil
}

// Both enrollment phases bind trusted MFA to the locked credential snapshot.
func enrollmentActorCurrent(c credential, a ext.Actor) bool {
	if a.MFA {
		return c.enabled && a.MFAGeneration > 0 && a.MFAGeneration == c.generation
	}
	return !c.enabled && c.generation == 0
}
