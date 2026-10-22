package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/config"
)

func adminFixture(t *testing.T) (*sql.DB, Policy, Plan, string) {
	t.Helper()
	dsn := os.Getenv("EXTENSION_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("EXTENSION_ADMIN_TEST_DSN required for disposable PostgreSQL")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "admin_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	q.Set("application_name", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	_, err = db.Exec(`CREATE TABLE organizations(id uuid PRIMARY KEY,deleted_at timestamptz);
 CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid NOT NULL,role_id uuid,is_active boolean NOT NULL, password_hash text,auth_stamp text,updated_at timestamptz,deleted_at timestamptz);
 CREATE TABLE user_organizations(user_id uuid,organization_id uuid,role_id uuid,updated_at timestamptz,deleted_at timestamptz);
 CREATE TABLE custom_roles(id uuid PRIMARY KEY,organization_id uuid,name text,description text,is_system boolean,is_default boolean,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz);
 CREATE TABLE permissions(id uuid PRIMARY KEY,resource text,action text,description text,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz, UNIQUE(resource,action));
 CREATE TABLE role_permissions(custom_role_id uuid,permission_id uuid,PRIMARY KEY(custom_role_id,permission_id))`)
	if err != nil {
		t.Fatal(err)
	}
	policy, plan := fixturePlan()
	oldRole := uuid.NewString()
	plan.ExpectedRoleID = &oldRole
	if _, err := db.Exec(`INSERT INTO organizations(id) VALUES($1)`, policy.OrganizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO custom_roles(id,organization_id,name) VALUES($1,$2,'Existing shared role')`, oldRole, policy.OrganizationID); err != nil {
		t.Fatal(err)
	}
	other := uuid.NewString()
	for _, id := range []string{policy.OperatorID, plan.UserID, other} {
		if _, err := db.Exec(`INSERT INTO users(id,organization_id,role_id,is_active,password_hash,auth_stamp) VALUES($1,$2,$3,true,'unchanged-password-hash','unchanged-auth-stamp')`, id, policy.OrganizationID, oldRole); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO user_organizations(user_id,organization_id,role_id) VALUES($1,$2,$3)`, id, policy.OrganizationID, oldRole); err != nil {
			t.Fatal(err)
		}
	}
	if err := (api.AccessStore{DB: db}).Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db, policy, plan, other
}

func employeeFixture(t *testing.T) (*sql.DB, Policy, Plan, *config.Config, *directory.PostgresStore) {
	t.Helper()
	db, policy, plan, _ := adminFixture(t)
	profile := policy.Profiles[plan.Profile]
	profile.SubjectKind = "employee"
	policy.Profiles[plan.Profile] = profile
	delete(policy.NonemployeeOperators, plan.UserID)
	policy.EmployeeBindings = map[string]directory.IdentityBinding{plan.UserID: {EmployeeID: "employee-17", Phone: "+27820000017", ExpectedRosterVersion: 1}}
	store, err := directory.NewPostgresStore(db, policy.OrganizationID, policy.OperatorID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	preview, err := directory.NewPreview([]directory.Employee{{ID: "employee-17", Phone: "+27820000017", Active: true, Zone: "north"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := directory.Replace(directory.Snapshot{}, preview, preview.Digest(), time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(ctx, 0, snapshot); err != nil {
		t.Fatal(err)
	}
	return db, policy, plan, &config.Config{Extensions: config.ExtensionsConfig{RosterFreshnessSeconds: 3600}}, store
}

func TestEmployeeActivationAppendsOwnerValidatedIdentityAtomically(t *testing.T) {
	db, policy, plan, cfg, _ := employeeFixture(t)
	ctx := context.Background()
	// Historical identities must be retained while the current roster identity
	// is appended, so older subject/conflict exclusions cannot disappear.
	access := api.AccessStore{DB: db}
	if err := access.LinkIdentity(ctx, policy.OrganizationID, plan.UserID, "historical-employee-4"); err != nil {
		t.Fatal(err)
	}
	if _, err := executePlan(ctx, db, cfg, policy, plan, false, "policyhash", "planhash"); err != nil {
		t.Fatal(err)
	}
	assertNoWrites(t, db, plan)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_identity_links`).Scan(&count); err != nil || count != 1 {
		t.Fatal("preview appended identity")
	}
	out, err := executePlan(ctx, db, cfg, policy, plan, true, "policyhash", "planhash")
	if err != nil || out.Access.Version != 1 {
		t.Fatalf("employee apply %+v %v", out, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_identity_links WHERE user_id=$1`, plan.UserID).Scan(&count); err != nil || count != 2 {
		t.Fatal("historical identity lost or current identity absent")
	}
	var metadata string
	if err := db.QueryRow(`SELECT metadata::text FROM addon_api_access_audit WHERE user_id=$1`, plan.UserID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var proof map[string]any
	if err := json.Unmarshal([]byte(metadata), &proof); err != nil {
		t.Fatal(err)
	}
	if proof["approved_roster_version"] != float64(1) || len(proof["employee_identity_sha256"].(string)) != 64 || strings.Contains(metadata, "+27820000017") {
		t.Fatalf("invalid/redaction failure in proof audit: %s", metadata)
	}
	version := int64(1)
	plan.ExpectedVersion = &version
	plan.ExpectedRoleID = &out.CurrentRoleID
	plan.Action = "revoke"
	plan.Profile = ""
	if _, err := executePlan(ctx, db, cfg, policy, plan, true, "policyhash", "revokehash"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_identity_links WHERE user_id=$1`, plan.UserID).Scan(&count); err != nil || count != 2 {
		t.Fatal("revocation erased conflict identities")
	}
}

func TestEmployeeActivationRejectsStaleUnknownOrConflictingIdentity(t *testing.T) {
	for _, name := range []string{"wrong-phone", "stale-version", "unknown-employee", "expired-roster", "revoked", "other-user-link", "cross-org-user-link", "audit-failure"} {
		t.Run(name, func(t *testing.T) {
			db, policy, plan, cfg, store := employeeFixture(t)
			binding := policy.EmployeeBindings[plan.UserID]
			switch name {
			case "wrong-phone":
				binding.Phone = "+27820000018"
			case "stale-version":
				binding.ExpectedRosterVersion = 2
			case "unknown-employee":
				binding.EmployeeID = "unknown"
			case "expired-roster":
				cfg.Extensions.RosterFreshnessSeconds = 1
			case "revoked":
				snapshot, err := store.Load(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				next, err := directory.Revoke(snapshot, binding.EmployeeID)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.CompareAndSwap(context.Background(), snapshot.Version, next); err != nil {
					t.Fatal(err)
				}
				binding.ExpectedRosterVersion = 2
			case "other-user-link":
				if err := (api.AccessStore{DB: db}).LinkIdentity(context.Background(), policy.OrganizationID, uuid.NewString(), binding.EmployeeID); err != nil {
					t.Fatal(err)
				}
			case "cross-org-user-link":
				if err := (api.AccessStore{DB: db}).LinkIdentity(context.Background(), uuid.NewString(), plan.UserID, binding.EmployeeID); err != nil {
					t.Fatal(err)
				}
			case "audit-failure":
				if _, err := db.Exec(`CREATE FUNCTION reject_employee_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$; CREATE TRIGGER reject_employee_audit BEFORE INSERT ON addon_api_access_audit FOR EACH ROW EXECUTE FUNCTION reject_employee_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			policy.EmployeeBindings[plan.UserID] = binding
			if _, err := executePlan(context.Background(), db, cfg, policy, plan, true, "policyhash", "planhash"); err == nil {
				t.Fatal("invalid employee activation succeeded")
			}
			assertNoWrites(t, db, plan)
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM addon_api_identity_links WHERE organization_id=$1 AND user_id=$2`, policy.OrganizationID, plan.UserID).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed activation leaked an identity link")
			}
		})
	}
}

func TestEmployeeConcurrentGrantCASHasOneAtomicWinner(t *testing.T) {
	db, policy, plan, cfg, _ := employeeFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			_, err := executePlan(context.Background(), db, cfg, policy, plan, true, "policyhash", "planhash")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("employee grant CAS winners=%d", winners)
	}
	for _, entry := range []struct {
		table string
		count int
	}{{"custom_roles", 2}, {"addon_api_identity_links", 1}, {"addon_api_access_versions", 1}, {"addon_api_access_audit", 1}, {"addon_api_grants", 1}} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + entry.table).Scan(&count); err != nil || count != entry.count {
			t.Fatalf("partial/provisional %s: count=%d err=%v", entry.table, count, err)
		}
	}
}

func waitForAdminFixtureLock(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=current_setting('application_name') AND wait_event_type='Lock' AND query LIKE $1`, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected PostgreSQL lock wait not observed: %s", query)
}

func TestEmployeeGrantFencesConcurrentOwnerRevocationThroughCommit(t *testing.T) {
	db, policy, plan, cfg, store := employeeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Pause only the host role INSERT, after the real CLI has obtained its
	// directory-owner shared proof lock and append-only identity transaction.
	if _, err := db.Exec(`CREATE FUNCTION pause_employee_role() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(current_setting('application_name')||':activation-fixture',0)); RETURN NEW; END $$; CREATE TRIGGER pause_employee_role BEFORE INSERT ON custom_roles FOR EACH ROW EXECUTE FUNCTION pause_employee_role()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback()
	if _, err := barrier.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('application_name')||':activation-fixture',0))`); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result Result
		err    error
	}
	grant := make(chan outcome, 1)
	go func() {
		out, err := executePlan(ctx, db, cfg, policy, plan, true, "policyhash", "planhash")
		grant <- outcome{out, err}
	}()
	waitForAdminFixtureLock(t, db, "INSERT INTO custom_roles%")
	current, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next, err := directory.Revoke(current, "employee-17")
	if err != nil {
		t.Fatal(err)
	}
	revocation := make(chan error, 1)
	go func() { revocation <- store.CompareAndSwap(ctx, current.Version, next) }()
	waitForAdminFixtureLock(t, db, "UPDATE directory_snapshots%")
	if err := barrier.Rollback(); err != nil {
		t.Fatal(err)
	}
	out := <-grant
	if out.err != nil {
		t.Fatal(out.err)
	}
	if err := <-revocation; err != nil {
		t.Fatal(err)
	}
	version := int64(1)
	plan.ExpectedVersion = &version
	plan.ExpectedRoleID = &out.result.CurrentRoleID
	binding := policy.EmployeeBindings[plan.UserID]
	binding.ExpectedRosterVersion = 2
	policy.EmployeeBindings[plan.UserID] = binding
	if _, err := executePlan(ctx, db, cfg, policy, plan, true, "policyhash", "newplanhash"); err == nil {
		t.Fatal("subsequent grant ignored committed revocation")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_access_audit`).Scan(&count); err != nil || count != 1 {
		t.Fatal("revoked second grant changed access/audit")
	}
}

func assertNoWrites(t *testing.T, db *sql.DB, plan Plan) {
	t.Helper()
	var role, password, stamp string
	if err := db.QueryRow(`SELECT role_id::text,password_hash,auth_stamp FROM users WHERE id=$1`, plan.UserID).Scan(&role, &password, &stamp); err != nil {
		t.Fatal(err)
	}
	if role != *plan.ExpectedRoleID || password != "unchanged-password-hash" || stamp != "unchanged-auth-stamp" {
		t.Fatal("identity or role changed without successful explicit apply")
	}
	for _, table := range []string{"addon_api_access_audit", "addon_api_access_versions", "addon_api_grants"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unexpected %s writes: %d %v", table, count, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM custom_roles`).Scan(&count); err != nil || count != 1 {
		t.Fatal("provisional role escaped transaction")
	}
}

func TestAdminPreviewApplyInspectAndRevoke(t *testing.T) {
	db, policy, plan, other := adminFixture(t)
	ctx := context.Background()
	before, err := executePlan(ctx, db, &config.Config{}, policy, plan, false, "policyhash", "planhash")
	if err != nil || before.Applied || before.Access.Version != 0 {
		t.Fatalf("preview: %+v %v", before, err)
	}
	assertNoWrites(t, db, plan)
	after, err := executePlan(ctx, db, &config.Config{}, policy, plan, true, "policyhash", "planhash")
	if err != nil || !after.Applied || after.Access.Version != 1 || after.CurrentRoleID == *plan.ExpectedRoleID {
		t.Fatalf("apply: %+v %v", after, err)
	}
	var role, password, stamp string
	if err := db.QueryRow(`SELECT role_id::text,password_hash,auth_stamp FROM users WHERE id=$1`, plan.UserID).Scan(&role, &password, &stamp); err != nil {
		t.Fatal(err)
	}
	if role != after.CurrentRoleID || password != "unchanged-password-hash" || stamp != "unchanged-auth-stamp" {
		t.Fatal("credentials changed or target role absent")
	}
	if err := db.QueryRow(`SELECT role_id::text FROM users WHERE id=$1`, other).Scan(&role); err != nil || role != *plan.ExpectedRoleID {
		t.Fatal("shared role user changed")
	}
	if _, err := executePlan(ctx, db, &config.Config{}, policy, plan, true, "policyhash", "planhash"); err == nil {
		t.Fatal("stale apply accepted")
	}
	current, err := inspect(ctx, db, policy, plan.UserID)
	if err != nil || current.Access.Version != 1 || current.CurrentRoleID != after.CurrentRoleID || len(current.Permissions) != 3 {
		t.Fatalf("inspect %+v %v", current, err)
	}
	version := int64(1)
	plan.ExpectedVersion = &version
	plan.ExpectedRoleID = &current.CurrentRoleID
	plan.Action = "revoke"
	plan.Profile = ""
	revoked, err := executePlan(ctx, db, &config.Config{}, policy, plan, true, "policyhash", "revokehash")
	if err != nil || revoked.Access.Version != 2 || len(revoked.Access.Grants) != 0 || len(revoked.Permissions) != 0 {
		t.Fatalf("revoke %+v %v", revoked, err)
	}
	current, err = inspect(ctx, db, policy, plan.UserID)
	if err != nil || len(current.Permissions) != 0 || len(current.Access.Grants) != 0 {
		t.Fatal("revocation retained grants/permissions")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_access_audit WHERE operator_id=$1 AND metadata->>'policy_sha256'='policyhash'`, policy.OperatorID).Scan(&count); err != nil || count != 2 {
		t.Fatal("missing named durable operator audit")
	}
}

func TestAdminRejectsCurrentMembershipRoleAndAuditFailure(t *testing.T) {
	for _, name := range []string{"disabled-target", "deleted-operator", "cross-org-target", "changed-role", "audit-failure"} {
		t.Run(name, func(t *testing.T) {
			db, policy, plan, _ := adminFixture(t)
			switch name {
			case "disabled-target":
				db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, plan.UserID)
			case "deleted-operator":
				db.Exec(`UPDATE users SET deleted_at=clock_timestamp() WHERE id=$1`, policy.OperatorID)
			case "cross-org-target":
				db.Exec(`UPDATE users SET organization_id=$1 WHERE id=$2`, uuid.NewString(), plan.UserID)
			case "changed-role":
				empty := ""
				plan.ExpectedRoleID = &empty
			case "audit-failure":
				if _, err := db.Exec(`CREATE FUNCTION reject_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$; CREATE TRIGGER reject_admin_audit BEFORE INSERT ON addon_api_access_audit FOR EACH ROW EXECUTE FUNCTION reject_admin_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := executePlan(context.Background(), db, &config.Config{}, policy, plan, true, "policyhash", "planhash"); err == nil {
				t.Fatal("boundary failure accepted")
			}
			if name == "changed-role" {
				var original string
				if err := db.QueryRow(`SELECT role_id::text FROM users WHERE id=$1`, plan.UserID).Scan(&original); err != nil {
					t.Fatal(err)
				}
				plan.ExpectedRoleID = &original
			}
			assertNoWrites(t, db, plan)
		})
	}
}

func TestWorkerProfilesRequireRuntimeBinding(t *testing.T) {
	id := uuid.NewString()
	if err := checkRuntimeBinding(&config.Config{}, Profile{Kind: "routing"}, id); err == nil {
		t.Fatal("unbound routing worker accepted")
	}
	if err := checkRuntimeBinding(&config.Config{Extensions: config.ExtensionsConfig{RoutingPrimaryIDs: id}}, Profile{Kind: "routing"}, id); err != nil {
		t.Fatal(err)
	}
	if err := checkRuntimeBinding(&config.Config{}, Profile{Kind: "escalation"}, id); err == nil {
		t.Fatal("unbound escalation worker accepted")
	}
}

func TestHumanIdentityCannotBeInventedOrDowngradedToVendor(t *testing.T) {
	for _, name := range []string{"employee-unlinked", "employee-historical-only", "vendor-with-employee-link"} {
		t.Run(name, func(t *testing.T) {
			db, policy, plan, _ := adminFixture(t)
			if name != "vendor-with-employee-link" {
				p := policy.Profiles[plan.Profile]
				p.SubjectKind = "employee"
				policy.Profiles[plan.Profile] = p
			}
			if name != "employee-unlinked" {
				if err := (api.AccessStore{DB: db}).LinkIdentity(context.Background(), policy.OrganizationID, plan.UserID, "synthetic-employee-17"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := executePlan(context.Background(), db, &config.Config{}, policy, plan, true, "policyhash", "planhash"); err == nil {
				t.Fatal("human identity safety boundary bypassed")
			}
			assertNoWrites(t, db, plan)
		})
	}
}
