package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

// activate is deliberately separate from provision. The plan describes an
// existing inactive row; credentials are verified, never rotated or reencrypted.
func activate(ctx context.Context, db *sql.DB, c *config.Config, p Plan, namespace, attempt string, apply bool) (Result, error) {
	r := Result{OrganizationID: p.OrganizationID, AccountID: p.AccountID, Status: "active"}
	if db == nil || validPlan(p) != nil || protectedschema.Validate(c) != nil || len(c.App.EncryptionKey) < 32 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(namespace) {
		return r, refused
	}
	if apply {
		if reserveAttempt(attempt, r) != nil {
			return r, refused
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: !apply})
	if err != nil {
		return r, refused
	}
	defer tx.Rollback()
	// A preview writes no rows/files, but must also refuse a guarded live runtime.
	var fenced bool
	if tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, protectedschema.LockID).Scan(&fenced) != nil || !fenced {
		return r, refused
	}
	var database, user, session, actualNS string
	var owner bool
	err = tx.QueryRowContext(ctx, `SELECT current_database(),current_user,session_user,current_schema(),EXISTS(SELECT 1 FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba WHERE d.datname=current_database() AND r.rolname=current_user)`).Scan(&database, &user, &session, &actualNS, &owner)
	if err != nil || database != c.Database.Name || user != c.Database.User || session != user || actualNS != namespace || !owner {
		return r, refused
	}
	var release, catalog string
	if tx.QueryRowContext(ctx, `SELECT release,catalog_sha256 FROM whatomate_schema_seal ORDER BY generation DESC LIMIT 1`).Scan(&release, &catalog) != nil || release != protectedschema.ReleaseIdentity || catalog != protectedschema.ExpectedCatalogSHA256 {
		return r, refused
	}
	var protected, pending bool
	if tx.QueryRowContext(ctx, `SELECT protected FROM whatomate_protection_state WHERE id=1`).Scan(&protected) != nil || !protected {
		return r, refused
	}
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM whatomate_schema_attempt a WHERE a.catalog_sha256<>'baseline' AND NOT EXISTS(SELECT 1 FROM whatomate_schema_seal s WHERE s.release=a.release AND s.catalog_sha256=a.catalog_sha256))`).Scan(&pending) != nil || pending {
		return r, refused
	}
	actual, err := protectedschema.Catalog(ctx, db, namespace)
	if err != nil || actual.SHA256 != protectedschema.ExpectedCatalogSHA256 {
		return r, refused
	}
	lock := ""
	if apply {
		lock = " FOR SHARE"
	}
	var org, operator, operatorName string
	if tx.QueryRowContext(ctx, `SELECT id::text FROM organizations WHERE id=$1 AND deleted_at IS NULL`+lock, p.OrganizationID).Scan(&org) != nil {
		return r, refused
	}
	if tx.QueryRowContext(ctx, `SELECT id::text,full_name FROM users WHERE id=$1 AND organization_id=$2 AND is_active=true AND deleted_at IS NULL`+lock, p.OperatorID, p.OrganizationID).Scan(&operator, &operatorName) != nil {
		return r, refused
	}
	lock = ""
	if apply {
		lock = " FOR UPDATE"
	}
	var token, secret, verify string
	err = tx.QueryRowContext(ctx, `SELECT access_token,app_secret,webhook_verify_token FROM whatsapp_accounts WHERE id=$1 AND organization_id=$2 AND name=$3 AND app_id=$4 AND phone_id=$5 AND business_id=$6 AND api_version=$7 AND status='inactive' AND deleted_at IS NULL`+lock, p.AccountID, p.OrganizationID, p.Name, p.AppID, p.PhoneID, p.BusinessID, p.APIVersion).Scan(&token, &secret, &verify)
	if err != nil || !strings.HasPrefix(token, "enc:") || !strings.HasPrefix(secret, "enc:") {
		return r, refused
	}
	plainToken, err := crypto.Decrypt(token, c.App.EncryptionKey)
	if err != nil {
		return r, refused
	}
	plainSecret, err := crypto.Decrypt(secret, c.App.EncryptionKey)
	if err != nil {
		return r, refused
	}
	if subtle.ConstantTimeCompare([]byte(plainToken), []byte(p.AccessToken)) != 1 || subtle.ConstantTimeCompare([]byte(plainSecret), []byte(p.AppSecret)) != 1 || subtle.ConstantTimeCompare([]byte(verify), []byte(p.VerifyToken)) != 1 {
		return r, refused
	}
	if !apply {
		if tx.Commit() != nil {
			return r, refused
		}
		return r, nil
	}
	updated, err := tx.ExecContext(ctx, `UPDATE whatsapp_accounts SET status='active',updated_by_id=$2,updated_at=$3 WHERE id=$1 AND status='inactive' AND deleted_at IS NULL`, p.AccountID, p.OperatorID, time.Now().UTC())
	if err != nil {
		return r, refused
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return r, refused
	}
	changes, _ := json.Marshal([]map[string]string{{"field": "status", "old": "inactive", "new": "active"}, {"field": "operation", "new": "offline_carrier_account_activate"}})
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at) VALUES($1,$2,'whatsapp_account',$3,$4,$5,'update',$6,$7)`, uuid.NewString(), p.OrganizationID, p.AccountID, p.OperatorID, operatorName, string(changes), time.Now().UTC())
	if err != nil || tx.Commit() != nil {
		return r, refused
	}
	r.Applied = true
	return r, nil
}
