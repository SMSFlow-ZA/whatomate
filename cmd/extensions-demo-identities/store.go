package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"golang.org/x/crypto/bcrypt"
	"regexp"
	"strings"
)

var refused = errors.New("synthetic_demo_identity_refused_or_unknown_preserve_attempt_no_retry")

func begin(ctx context.Context, db *sql.DB, c *config.Config, p Plan, namespace string, apply bool) (*sql.Tx, string, error) {
	if db == nil || validPlan(p) != nil || protectedschema.Validate(c) != nil || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(namespace) || c.Database.Name != "er_"+strings.ReplaceAll(p.InstanceID, "-", "") || c.Database.User != "er_owner_"+strings.ReplaceAll(p.InstanceID, "-", "") {
		return nil, "", refused
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: !apply})
	if e != nil {
		return nil, "", refused
	}
	success := false
	defer func() {
		if !success {
			tx.Rollback()
		}
	}()
	// Same physical transaction retains a shared migration lease through commit.
	// It is compatible with the live runtime and blocks the exclusive installer.
	var held bool
	if tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock_shared($1)`, protectedschema.LockID).Scan(&held) != nil || !held {
		return nil, "", refused
	}
	var identity bool
	var ns string
	if tx.QueryRowContext(ctx, `SELECT current_database()=$1 AND current_user=$2 AND session_user=current_user AND EXISTS(SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND d.datdba=(SELECT oid FROM pg_roles WHERE rolname=current_user)),current_schema()`, c.Database.Name, c.Database.User).Scan(&identity, &ns) != nil || !identity || ns != namespace {
		return nil, "", refused
	}
	var generation int64
	var release, catalog string
	if tx.QueryRowContext(ctx, `SELECT generation,release,catalog_sha256 FROM whatomate_schema_seal ORDER BY generation DESC LIMIT 1`).Scan(&generation, &release, &catalog) != nil || generation != p.Generation || release != p.Release || catalog != p.CatalogSHA256 {
		return nil, "", refused
	}
	var protected, pending bool
	if tx.QueryRowContext(ctx, `SELECT protected FROM whatomate_protection_state WHERE id=1`).Scan(&protected) != nil || !protected {
		return nil, "", refused
	}
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM whatomate_schema_attempt a WHERE a.catalog_sha256<>'baseline' AND NOT EXISTS(SELECT 1 FROM whatomate_schema_seal s WHERE s.release=a.release AND s.catalog_sha256=a.catalog_sha256))`).Scan(&pending) != nil || pending {
		return nil, "", refused
	}
	actual, e := protectedschema.Catalog(ctx, db, namespace)
	if e != nil || actual.SHA256 != p.CatalogSHA256 {
		return nil, "", refused
	}
	rowLock := ""
	if apply {
		rowLock = " FOR SHARE OF o,u,m"
	}
	var ownerName string
	if tx.QueryRowContext(ctx, `SELECT u.full_name FROM organizations o JOIN users u ON u.organization_id=o.id JOIN user_organizations m ON m.user_id=u.id AND m.organization_id=o.id WHERE o.id=$1 AND u.id=$2 AND o.deleted_at IS NULL AND u.deleted_at IS NULL AND u.is_active AND m.deleted_at IS NULL AND m.is_default`+rowLock, p.OrganizationID, p.OperatorID).Scan(&ownerName) != nil {
		return nil, "", refused
	}

	success = true
	return tx, ownerName, nil
}
func resultFor(p Plan, hash, outcome string) Result {
	r := Result{OrganizationID: p.OrganizationID, PlanSHA256: hash, Outcome: outcome, SyntheticFixtures: true, PrimaryOnly: true}
	for _, id := range p.Identities {
		r.Identities = append(r.Identities, IdentityResult{Fixture: id.Fixture, UserID: id.UserID, MembershipID: id.MembershipID})
	}
	return r
}
func emptyAuthority(ctx context.Context, tx *sql.Tx, id string) bool {
	var occupied bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys WHERE user_id=$1::uuid) OR EXISTS(SELECT 1 FROM addon_api_identity_links WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_api_grants WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_api_access_versions WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_api_access_audit WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM extension_security_mfa WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_knowledge_grants WHERE principal=$1::text)`, id).Scan(&occupied)
	return e == nil && !occupied
}
func provision(ctx context.Context, db *sql.DB, c *config.Config, p Plan, namespace string, passwords map[string][]byte, apply bool, planSHA string) (Result, error) {
	r := resultFor(p, planSHA, "refused")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(planSHA) {
		return r, refused
	}
	if apply {
		if len(passwords) != len(fixtureLabels) {
			return r, refused
		}
		for _, v := range p.Identities {
			if len(passwords[v.Fixture]) < 32 || len(passwords[v.Fixture]) > 64 {
				return r, refused
			}
		}
	}
	tx, owner, e := begin(ctx, db, c, p, namespace, apply)
	if e != nil {
		return r, refused
	}
	defer tx.Rollback()
	for _, v := range p.Identities {
		var occupied bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 OR email=$2) OR EXISTS(SELECT 1 FROM user_organizations WHERE id=$3 OR user_id=$1)`, v.UserID, v.Email, v.MembershipID).Scan(&occupied) != nil || occupied || !emptyAuthority(ctx, tx, v.UserID) {
			return r, refused
		}
	}
	if !apply {
		if tx.Commit() != nil {
			return r, refused
		}
		r.Outcome = "preview-validated"
		return r, nil
	}
	for _, v := range p.Identities {
		hash, e := bcrypt.GenerateFromPassword(passwords[v.Fixture], bcrypt.DefaultCost)
		if e != nil {
			return r, refused
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO users(id,organization_id,email,auth_stamp,password_hash,full_name,role_id,settings,is_active,is_available,is_super_admin,sso_provider,sso_binding,sso_provider_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,NULL,'{}',true,false,false,'','','',clock_timestamp(),clock_timestamp())`, v.UserID, p.OrganizationID, v.Email, uuid.NewString(), string(hash), "SYNTHETIC ROLE ACCEPTANCE: "+v.Fixture); e != nil {
			return r, refused
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO user_organizations(id,user_id,organization_id,role_id,is_default,created_at,updated_at) VALUES($1,$2,$3,NULL,true,clock_timestamp(),clock_timestamp())`, v.MembershipID, v.UserID, p.OrganizationID); e != nil {
			return r, refused
		}
		changes := auditChanges(p, v, planSHA)
		if _, e = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at) VALUES($1,$2,'user',$3,$4,$5,'create',$6,clock_timestamp())`, uuid.NewString(), p.OrganizationID, v.UserID, p.OperatorID, owner, string(changes)); e != nil {
			return r, refused
		}
	}
	if tx.Commit() != nil {
		r.Outcome = "unknown-held"
		return r, refused
	}
	observed, e := reconcile(ctx, db, c, p, namespace, passwords, planSHA)
	if e != nil {
		r.Outcome = "unknown-held"
		return r, refused
	}
	observed.Applied = true
	return observed, nil
}
func reconcile(ctx context.Context, db *sql.DB, c *config.Config, p Plan, namespace string, passwords map[string][]byte, planSHA string) (Result, error) {
	r := resultFor(p, planSHA, "unknown-held")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(planSHA) || len(passwords) != len(fixtureLabels) {
		return r, refused
	}
	tx, _, e := begin(ctx, db, c, p, namespace, false)
	if e != nil {
		return r, refused
	}
	defer tx.Rollback()
	for _, v := range p.Identities {
		var hash string
		var exact bool
		e = tx.QueryRowContext(ctx, `SELECT password_hash,email=$3 AND full_name=$4 AND is_active AND NOT is_available AND NOT is_super_admin AND role_id IS NULL AND sso_provider='' AND sso_binding='' AND sso_provider_id='' AND auth_stamp<>'' FROM users WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL`, v.UserID, p.OrganizationID, v.Email, "SYNTHETIC ROLE ACCEPTANCE: "+v.Fixture).Scan(&hash, &exact)
		if e != nil || !exact || len(passwords[v.Fixture]) < 32 || bcrypt.CompareHashAndPassword([]byte(hash), passwords[v.Fixture]) != nil || !emptyAuthority(ctx, tx, v.UserID) {
			return r, refused
		}
		var members int
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM user_organizations WHERE user_id=$1`, v.UserID).Scan(&members) != nil || members != 1 {
			return r, refused
		}
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_organizations WHERE id=$1 AND user_id=$2 AND organization_id=$3 AND role_id IS NULL AND is_default AND deleted_at IS NULL)`, v.MembershipID, v.UserID, p.OrganizationID).Scan(&exact) != nil || !exact {
			return r, refused
		}
		if tx.QueryRowContext(ctx, `SELECT count(*)=1 AND coalesce(bool_and(organization_id=$2 AND user_id=$3 AND resource_type='user' AND action='create' AND changes=$4::jsonb),false) FROM audit_logs WHERE resource_id=$1`, v.UserID, p.OrganizationID, p.OperatorID, string(auditChanges(p, v, planSHA))).Scan(&exact) != nil || !exact {
			return r, refused
		}
	}
	if tx.Commit() != nil {
		return r, refused
	}
	r.Outcome = "committed-observed"
	return r, nil
}

func auditChanges(p Plan, v Identity, planSHA string) []byte {
	raw, _ := json.Marshal([]map[string]string{{"field": "operation", "new": p.Purpose}, {"field": "fixture", "new": v.Fixture}, {"field": "attempt_nonce", "new": p.AttemptNonce}, {"field": "plan_sha256", "new": planSHA}, {"field": "classification", "new": "synthetic fixture; no real employee identity or human approval"}, {"field": "permissions", "new": "none; separate reviewed authorization required"}})
	return raw
}
