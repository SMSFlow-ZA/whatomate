//go:build integration

package security

import (
	"context"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func securityFixture(t *testing.T) (*Service, *sql.DB, ext.Actor, *atomic.Int64) {
	t.Helper()
	dsn := os.Getenv("EXTENSION_DATABASE_URL")
	if dsn == "" {
		t.Skip("EXTENSION_DATABASE_URL not supplied")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	clock := &atomic.Int64{}
	clock.Store(1800000000)
	s, e := New(db, key, Config{Drift: 1, EnrollmentTTL: 10 * time.Minute, MaxBreakGlass: 30 * time.Minute, AllowedBreakGlassPermissions: []string{"cases:read"}, Clock: func() time.Time { return time.Unix(clock.Load(), 0) }})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	actor := ext.Actor{OrganizationID: "security-" + time.Now().Format("150405.000000000"), UserID: "named-user", SessionID: "session", Permissions: []string{EnrollPermission, RevokePermission, BreakGlassRequestPermission, BreakGlassApprovePermission, BreakGlassUsePermission, BreakGlassRevokePermission}}
	return s, db, actor, clock
}
func enroll(t *testing.T, s *Service, actor ext.Actor, now int64) (Activation, []byte) {
	t.Helper()
	enrollment, e := s.BeginEnrollment(context.Background(), actor)
	if e != nil {
		t.Fatal(e)
	}
	seed, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret().Reveal())
	if e != nil {
		t.Fatal(e)
	}
	activation, e := s.ConfirmEnrollment(context.Background(), actor, enrollment.ID(), totp(seed, uint64(now/30)))
	if e != nil {
		t.Fatal(e)
	}
	return activation, seed
}
func TestSQLTOTPAndRecoverySingleUseAndCredentialRevocation(t *testing.T) {
	s, db, actor, clock := securityFixture(t)
	ctx := context.Background()
	activation, seed := enroll(t, s, actor, clock.Load())
	generation := activation.Verification().Generation()
	if generation != 1 || len(activation.RecoveryCodes()) != 10 {
		t.Fatal("activation generation/recovery count")
	}
	if _, e := s.VerifyTOTP(ctx, actor.OrganizationID, actor.UserID, totp(seed, uint64(clock.Load()/30))); !errors.Is(e, ErrDenied) {
		t.Fatal("enrollment OTP replay accepted", e)
	}
	clock.Add(30)
	code := totp(seed, uint64(clock.Load()/30))
	var wg sync.WaitGroup
	wins := make(chan bool, 12)
	for range 12 {
		wg.Go(func() {
			proof, e := s.VerifyTOTP(ctx, actor.OrganizationID, actor.UserID, code)
			wins <- e == nil && proof.Generation() == generation
		})
	}
	wg.Wait()
	close(wins)
	n := 0
	for win := range wins {
		if win {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("TOTP CAS winners %d", n)
	}
	// Advance past lockout after intentionally replaying one code concurrently.
	clock.Add(301)
	recovery := activation.RecoveryCodes()[0].Reveal()
	wins = make(chan bool, 12)
	for range 12 {
		wg.Go(func() {
			proof, e := s.VerifyRecovery(ctx, actor.OrganizationID, actor.UserID, recovery)
			wins <- e == nil && proof.Generation() == generation
		})
	}
	wg.Wait()
	close(wins)
	n = 0
	for win := range wins {
		if win {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("recovery winners %d", n)
	}
	actor.MFA = true
	actor.MFAGeneration = 1
	view, e := s.Revoke(ctx, actor, generation)
	if e != nil || view.Generation() != 2 || view.Enabled() {
		t.Fatal("credential revocation", e)
	}
	if e = s.CheckGeneration(ctx, actor.OrganizationID, actor.UserID, generation); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked session generation accepted", e)
	}
	if _, e = s.VerifyRecovery(ctx, actor.OrganizationID, actor.UserID, activation.RecoveryCodes()[1].Reveal()); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked recovery accepted", e)
	}
	var ciphertext []byte
	if e = db.QueryRow("SELECT secret_cipher FROM extension_security_mfa WHERE organization_id=$1 AND user_id=$2", actor.OrganizationID, actor.UserID).Scan(&ciphertext); e != nil {
		t.Fatal(e)
	}
	if len(ciphertext) != 0 {
		t.Fatal("revoked secret retained")
	}
	raw, e := json.Marshal(activation)
	if e != nil || strings.Contains(string(raw), recovery) || strings.Contains(fmt.Sprintf("%+v", activation), recovery) {
		t.Fatal("activation leaked recovery")
	}
}
func TestSQLBreakGlassSeparateApprovalScopeExpiryAndRevocation(t *testing.T) {
	s, _, actor, clock := securityFixture(t)
	ctx := context.Background()
	activation, _ := enroll(t, s, actor, clock.Load())
	actor.MFA = true
	actor.MFAGeneration = 1
	grant, e := s.RequestBreakGlass(ctx, actor, "cases:case-1", "cases:read", "incident:synthetic", 20*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeBreakGlass(ctx, actor, grant.ID(), "cases:case-1", "cases:read"); !errors.Is(e, ErrDenied) {
		t.Fatal("unapproved grant active", e)
	}
	if _, e = s.ApproveBreakGlass(ctx, actor, grant.ID(), 1); !errors.Is(e, ErrDenied) {
		t.Fatal("self approval", e)
	}
	approver := actor
	approver.UserID = "separate-approver"
	approver.MFA = false
	enroll(t, s, approver, clock.Load())
	approver.MFA = true
	approver.MFAGeneration = 1
	grant, e = s.ApproveBreakGlass(ctx, approver, grant.ID(), 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeBreakGlass(ctx, actor, grant.ID(), "cases:case-1", "cases:read"); e != nil {
		t.Fatal(e)
	}
	for _, a := range []ext.Actor{{OrganizationID: actor.OrganizationID, UserID: "infrastructure-admin", MFA: true}, approver} {
		if e = s.AuthorizeBreakGlass(ctx, a, grant.ID(), "cases:case-1", "cases:read"); !errors.Is(e, ErrDenied) {
			t.Fatal("named user bypass", e)
		}
	}
	if e = s.AuthorizeBreakGlass(ctx, actor, grant.ID(), "cases:other", "cases:read"); !errors.Is(e, ErrDenied) {
		t.Fatal("cross-resource grant", e)
	}
	clock.Add(1200)
	if e = s.AuthorizeBreakGlass(ctx, actor, grant.ID(), "cases:case-1", "cases:read"); !errors.Is(e, ErrDenied) {
		t.Fatal("expiry boundary accepted", e)
	}
	if _, e = s.RequestBreakGlass(ctx, actor, "cases:case-1", "cases:read", "incident:x", 31*time.Minute); !errors.Is(e, ErrDenied) {
		t.Fatal("maximum TTL bypass", e)
	}
	clock.Add(30)
	grant, e = s.RequestBreakGlass(ctx, actor, "cases:case-1", "cases:read", "incident:new", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	grant, e = s.ApproveBreakGlass(ctx, approver, grant.ID(), 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Revoke(ctx, actor, activation.Verification().Generation()); e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeBreakGlass(ctx, actor, grant.ID(), "cases:case-1", "cases:read"); !errors.Is(e, ErrDenied) {
		t.Fatal("credential revocation did not revoke emergency access", e)
	}
}

func TestSQLAuditFailureRollsBackRecoveryConsumption(t *testing.T) {
	s, db, actor, clock := securityFixture(t)
	ctx := context.Background()
	activation, _ := enroll(t, s, actor, clock.Load())
	code := activation.RecoveryCodes()[0].Reveal()
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	trigger := "security_audit_fail_" + suffix
	function := trigger + "_fn"
	query := "CREATE FUNCTION " + function + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.organization_id='" + actor.OrganizationID + "' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER " + trigger + " BEFORE INSERT ON extension_security_audit FOR EACH ROW EXECUTE FUNCTION " + function + "()"
	if _, e := db.ExecContext(ctx, query); e != nil {
		t.Fatal(e)
	}
	cleanup := func() {
		db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+trigger+" ON extension_security_audit; DROP FUNCTION IF EXISTS "+function+"()")
	}
	t.Cleanup(cleanup)
	proof, e := s.VerifyRecovery(ctx, actor.OrganizationID, actor.UserID, code)
	if e == nil || proof.Generation() != 0 {
		t.Fatal("audit failure yielded MFA proof")
	}
	cleanup()
	proof, e = s.VerifyRecovery(ctx, actor.OrganizationID, actor.UserID, code)
	if e != nil || proof.Generation() != 1 {
		t.Fatal("failed audit consumed recovery code", e)
	}
}
func TestSQLExplicitBreakGlassRevocationAndPermissionAllowlist(t *testing.T) {
	s, _, actor, clock := securityFixture(t)
	ctx := context.Background()
	enroll(t, s, actor, clock.Load())
	actor.MFA = true
	actor.MFAGeneration = 1
	if _, e := s.RequestBreakGlass(ctx, actor, "cases:case-1", "cases:delete", "incident:synthetic", time.Minute); !errors.Is(e, ErrDenied) {
		t.Fatal("permission outside host allowlist", e)
	}
	grant, e := s.RequestBreakGlass(ctx, actor, "cases:case-1", "cases:read", "incident:synthetic", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	approver := actor
	approver.UserID = "approver"
	approver.MFA = false
	enroll(t, s, approver, clock.Load())
	approver.MFA = true
	approver.MFAGeneration = 1
	grant, e = s.ApproveBreakGlass(ctx, approver, grant.ID(), 1)
	if e != nil {
		t.Fatal(e)
	}
	grant, e = s.RevokeBreakGlass(ctx, approver, grant.ID(), grant.Version())
	if e != nil || grant.Version() != 3 {
		t.Fatal("explicit grant revocation", e)
	}
	if e = s.AuthorizeBreakGlass(ctx, actor, grant.ID(), "cases:case-1", "cases:read"); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked grant authorized", e)
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 10)
	grant, e = s.RequestBreakGlass(ctx, actor, "cases:case-1", "cases:read", "incident:concurrent", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	for range 10 {
		wg.Go(func() { _, e := s.ApproveBreakGlass(ctx, approver, grant.ID(), 1); wins <- e == nil })
	}
	wg.Wait()
	close(wins)
	n := 0
	for win := range wins {
		if win {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("separate approval winners %d", n)
	}
}

func TestSQLEnrollmentExpiryAndReplacementRequireVerifiedMFA(t *testing.T) {
	s, _, actor, clock := securityFixture(t)
	ctx := context.Background()
	enrollment, e := s.BeginEnrollment(ctx, actor)
	if e != nil {
		t.Fatal(e)
	}
	seed, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret().Reveal())
	if e != nil {
		t.Fatal(e)
	}
	clock.Add(600)
	if _, e = s.ConfirmEnrollment(ctx, actor, enrollment.ID(), totp(seed, uint64(clock.Load()/30))); !errors.Is(e, ErrDenied) {
		t.Fatal("expired enrollment activated", e)
	}
	status, e := s.Generation(ctx, actor.OrganizationID, actor.UserID)
	if e != nil || status.Enabled() || status.Generation() != 0 {
		t.Fatal("expired enrollment changed generation", e)
	}
	enroll(t, s, actor, clock.Load())
	if _, e = s.BeginEnrollment(ctx, actor); !errors.Is(e, ErrDenied) {
		t.Fatal("unverified replacement enrollment accepted", e)
	}
	if _, e = s.Revoke(ctx, actor, 1); !errors.Is(e, ErrDenied) {
		t.Fatal("unverified credential revocation accepted", e)
	}
	if _, e = s.RequestBreakGlass(ctx, actor, "cases:x", "cases:read", "incident:mfa", time.Minute); !errors.Is(e, ErrDenied) {
		t.Fatal("unverified MFA emergency request", e)
	}
}

// A trusted bridge may have read an MFA actor just before another request revokes
// it. The security transaction must reject that stale actor even when MFA=true.
func TestSQLStaleActorCannotEnrollAfterCredentialRevocation(t *testing.T) {
	s, _, actor, clock := securityFixture(t)
	activation, _ := enroll(t, s, actor, clock.Load())
	actor.MFA = true
	actor.MFAGeneration = activation.Verification().Generation()
	if _, err := s.Revoke(context.Background(), actor, activation.Verification().Generation()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginEnrollment(context.Background(), actor); !errors.Is(err, ErrDenied) {
		t.Fatalf("stale verified actor enrolled after revoke: %v", err)
	}
}
