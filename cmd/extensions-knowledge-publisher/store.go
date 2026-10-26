package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"golang.org/x/crypto/bcrypt"
)

var refused = errors.New("synthetic_publisher_refused_or_outcome_unknown_preserve_attempt_no_automatic_retry")

const syntheticName = "AUTOMATION SYNTHETIC KNOWLEDGE PUBLISHER"

type Plan struct {
	Version        int    `json:"version"`
	InstanceID     string `json:"instance_id"`
	OrganizationID string `json:"organization_id"`
	OperatorID     string `json:"operator_id"`
	UserID         string `json:"user_id"`
	MembershipID   string `json:"membership_id"`
	Email          string `json:"email"`
	Purpose        string `json:"purpose"`
	Generation     int64  `json:"generation"`
	Release        string `json:"release"`
	CatalogSHA256  string `json:"catalog_sha256"`
}
type Result struct {
	OrganizationID       string `json:"organization_id"`
	UserID               string `json:"user_id"`
	Applied              bool   `json:"applied"`
	AutomationSynthetic  bool   `json:"automation_synthetic"`
	PrimaryOnly          bool   `json:"primary_identity_only"`
	HumanApprovalClaimed bool   `json:"human_approval_claimed"`
}

func validPlan(p Plan) error {
	seen := map[string]bool{}
	for _, id := range []string{p.InstanceID, p.OrganizationID, p.OperatorID, p.UserID, p.MembershipID} {
		v, e := uuid.Parse(id)
		if e != nil || v == uuid.Nil || v.String() != id || seen[id] {
			return refused
		}
		seen[id] = true
	}
	if p.Version != 1 || p.Purpose != "automation-synthetic-knowledge-publisher" || !regexp.MustCompile(`^synthetic-knowledge-publisher-[a-f0-9]{8}@example\.invalid$`).MatchString(p.Email) || p.Generation < 1 || p.Release != protectedschema.ReleaseIdentity || p.CatalogSHA256 != protectedschema.ExpectedCatalogSHA256 || !regexp.MustCompile(`^[a-f0-9]{40}:[a-f0-9]{40}$`).MatchString(p.Release) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(p.CatalogSHA256) {
		return refused
	}
	return nil
}

// Only primary identity, membership and audit rows are inserted. No grants,
// roles, employee links, factors, API keys, schema or existing identity updates.
func provision(ctx context.Context, db *sql.DB, c *config.Config, p Plan, namespace string, password []byte, apply bool) (Result, error) {
	r := Result{OrganizationID: p.OrganizationID, UserID: p.UserID, AutomationSynthetic: true, PrimaryOnly: true}
	if db == nil || validPlan(p) != nil || protectedschema.Validate(c) != nil || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(namespace) || c.Database.Name != "er_"+strings.ReplaceAll(p.InstanceID, "-", "") || c.Database.User != "er_owner_"+strings.ReplaceAll(p.InstanceID, "-", "") || apply && (len(password) < 32 || len(password) > 64) {
		return r, refused
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: !apply})
	if e != nil {
		return r, refused
	}
	defer tx.Rollback()
	// Same physical transaction retains a shared migration lease through commit.
	// It is compatible with the live runtime and blocks the exclusive installer.
	var held bool
	if tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock_shared($1)`, protectedschema.LockID).Scan(&held) != nil || !held {
		return r, refused
	}
	var identity bool
	var ns string
	if tx.QueryRowContext(ctx, `SELECT current_database()=$1 AND current_user=$2 AND session_user=current_user AND EXISTS(SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND d.datdba=(SELECT oid FROM pg_roles WHERE rolname=current_user)),current_schema()`, c.Database.Name, c.Database.User).Scan(&identity, &ns) != nil || !identity || ns != namespace {
		return r, refused
	}
	var generation int64
	var release, catalog string
	if tx.QueryRowContext(ctx, `SELECT generation,release,catalog_sha256 FROM whatomate_schema_seal ORDER BY generation DESC LIMIT 1`).Scan(&generation, &release, &catalog) != nil || generation != p.Generation || release != p.Release || catalog != p.CatalogSHA256 {
		return r, refused
	}
	var protected, pending bool
	if tx.QueryRowContext(ctx, `SELECT protected FROM whatomate_protection_state WHERE id=1`).Scan(&protected) != nil || !protected {
		return r, refused
	}
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM whatomate_schema_attempt a WHERE a.catalog_sha256<>'baseline' AND NOT EXISTS(SELECT 1 FROM whatomate_schema_seal s WHERE s.release=a.release AND s.catalog_sha256=a.catalog_sha256))`).Scan(&pending) != nil || pending {
		return r, refused
	}
	actual, e := protectedschema.Catalog(ctx, db, namespace)
	if e != nil || actual.SHA256 != p.CatalogSHA256 {
		return r, refused
	}
	rowLock := ""
	if apply {
		rowLock = " FOR SHARE OF o,u"
	}
	var ownerName string
	if tx.QueryRowContext(ctx, `SELECT u.full_name FROM organizations o JOIN users u ON u.organization_id=o.id WHERE o.id=$1 AND u.id=$2 AND o.deleted_at IS NULL AND u.deleted_at IS NULL AND u.is_active`+rowLock, p.OrganizationID, p.OperatorID).Scan(&ownerName) != nil {
		return r, refused
	}
	var occupied bool
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 OR email=$2) OR EXISTS(SELECT 1 FROM user_organizations WHERE id=$3 OR user_id=$1) OR EXISTS(SELECT 1 FROM api_keys WHERE user_id=$1) OR EXISTS(SELECT 1 FROM addon_api_identity_links WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_api_grants WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_api_access_versions WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_api_access_audit WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM extension_security_mfa WHERE user_id=$1::text) OR EXISTS(SELECT 1 FROM addon_knowledge_grants WHERE principal=$1::text)`, p.UserID, p.Email, p.MembershipID).Scan(&occupied) != nil || occupied {
		return r, refused
	}
	if !apply {
		return r, tx.Commit()
	}
	hash, e := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if e != nil {
		return r, refused
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO users(id,organization_id,email,auth_stamp,password_hash,full_name,role_id,settings,is_active,is_available,is_super_admin,sso_provider,sso_binding,sso_provider_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,NULL,'{}',true,false,false,'','','',clock_timestamp(),clock_timestamp())`, p.UserID, p.OrganizationID, p.Email, uuid.NewString(), string(hash), syntheticName); e != nil {
		return r, refused
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO user_organizations(id,user_id,organization_id,role_id,is_default,created_at,updated_at) VALUES($1,$2,$3,NULL,true,clock_timestamp(),clock_timestamp())`, p.MembershipID, p.UserID, p.OrganizationID); e != nil {
		return r, refused
	}
	changes, _ := json.Marshal([]map[string]string{{"field": "operation", "new": p.Purpose}, {"field": "classification", "new": "automation-only synthetic demonstration; no human or client approval claim"}, {"field": "permissions", "new": "none; separate reviewed extensions-admin required"}})
	if _, e = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at) VALUES($1,$2,'user',$3,$4,$5,'create',$6,clock_timestamp())`, uuid.NewString(), p.OrganizationID, p.UserID, p.OperatorID, ownerName, string(changes)); e != nil {
		return r, refused
	}
	if e = tx.Commit(); e != nil {
		return r, refused
	}
	r.Applied = true
	return r, nil
}
