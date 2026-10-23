package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

var refused = errors.New("carrier account refused or outcome unknown; retain attempt and inspect privately; never retry automatically")

type Plan struct {
	SchemaVersion  int    `json:"schema_version"`
	OrganizationID string `json:"organization_id"`
	AccountID      string `json:"account_id"`
	OperatorID     string `json:"operator_id"`
	Name           string `json:"name"`
	AppID          string `json:"app_id"`
	PhoneID        string `json:"phone_id"`
	BusinessID     string `json:"business_id"`
	APIVersion     string `json:"api_version"`
	AccessToken    string `json:"access_token"`
	AppSecret      string `json:"app_secret"`
	VerifyToken    string `json:"webhook_verify_token"`
}
type Result struct {
	OrganizationID string `json:"organization_id"`
	AccountID      string `json:"account_id"`
	Applied        bool   `json:"applied"`
	Status         string `json:"status"`
	NetworkCalls   bool   `json:"provider_calls"`
}

func validPlan(p Plan) error {
	for _, id := range []string{p.OrganizationID, p.AccountID, p.OperatorID} {
		v, e := uuid.Parse(id)
		if e != nil || v == uuid.Nil || v.String() != id {
			return refused
		}
	}
	digits := regexp.MustCompile(`^[1-9][0-9]{0,99}$`)
	if p.SchemaVersion != 1 || strings.TrimSpace(p.Name) != p.Name || p.Name == "" || len(p.Name) > 100 || !digits.MatchString(p.AppID) || !digits.MatchString(p.PhoneID) || !digits.MatchString(p.BusinessID) || !regexp.MustCompile(`^v[1-9][0-9]?\.[0-9]{1,2}$`).MatchString(p.APIVersion) {
		return refused
	}
	for i, v := range []string{p.AccessToken, p.AppSecret, p.VerifyToken} {
		max := 128
		if i == 0 {
			max = 16384
		}
		if len(v) < 12 || len(v) > max || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") {
			return refused
		}
	}
	return nil
}

// No migration, grants, activation, credential refresh or provider constructor.
// All account/audit writes use the transaction's one physical owner session.
func provision(ctx context.Context, db *sql.DB, c *config.Config, p Plan, namespace, attempt string, apply bool) (Result, error) {
	r := Result{OrganizationID: p.OrganizationID, AccountID: p.AccountID, Status: "inactive"}
	if db == nil || validPlan(p) != nil || protectedschema.Validate(c) != nil || len(c.App.EncryptionKey) < 32 || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(namespace) {
		return r, refused
	}
	if apply {
		if e := reserveAttempt(attempt, r); e != nil {
			return r, refused
		}
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: !apply})
	if e != nil {
		return r, refused
	}
	defer tx.Rollback()
	if apply {
		var held bool
		if e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, protectedschema.LockID).Scan(&held); e != nil || !held {
			return r, refused
		}
	}
	var name, user, session, actualNS string
	var owner bool
	e = tx.QueryRowContext(ctx, `SELECT current_database(),current_user,session_user,current_schema(),EXISTS(SELECT 1 FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba WHERE d.datname=current_database() AND r.rolname=current_user)`).Scan(&name, &user, &session, &actualNS, &owner)
	if e != nil || name != c.Database.Name || user != c.Database.User || session != user || actualNS != namespace || !owner {
		return r, refused
	}
	var release, shape string
	if e = tx.QueryRowContext(ctx, `SELECT release,catalog_sha256 FROM whatomate_schema_seal ORDER BY generation DESC LIMIT 1`).Scan(&release, &shape); e != nil || release != protectedschema.ReleaseIdentity || shape != protectedschema.ExpectedCatalogSHA256 {
		return r, refused
	}
	var protected, pending bool
	if e = tx.QueryRowContext(ctx, `SELECT protected FROM whatomate_protection_state WHERE id=1`).Scan(&protected); e != nil || !protected {
		return r, refused
	}
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM whatomate_schema_attempt a WHERE a.catalog_sha256<>'baseline' AND NOT EXISTS(SELECT 1 FROM whatomate_schema_seal s WHERE s.release=a.release AND s.catalog_sha256=a.catalog_sha256))`).Scan(&pending); e != nil || pending {
		return r, refused
	}
	// Capture is read-only on a second connection while this transaction retains
	// the exclusive owner fence. No owner migration or schema installation occurs.
	actual, e := protectedschema.Catalog(ctx, db, namespace)
	if e != nil || actual.SHA256 != protectedschema.ExpectedCatalogSHA256 {
		return r, refused
	}
	// Hold source identities through commit. No role, membership, MFA or grants change.
	var org, operator, operatorName string
	rowLock := ""
	if apply {
		rowLock = " FOR SHARE"
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM organizations WHERE id=$1 AND deleted_at IS NULL`+rowLock, p.OrganizationID).Scan(&org); e != nil {
		return r, refused
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text,full_name FROM users WHERE id=$1 AND organization_id=$2 AND is_active=true AND deleted_at IS NULL`+rowLock, p.OperatorID, p.OrganizationID).Scan(&operator, &operatorName); e != nil {
		return r, refused
	}
	var occupied bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM whatsapp_accounts WHERE id=$1 OR phone_id=$2 OR business_id=$3 OR (organization_id=$4 AND name=$5))`, p.AccountID, p.PhoneID, p.BusinessID, p.OrganizationID, p.Name).Scan(&occupied); e != nil || occupied {
		return r, refused
	}
	if !apply {
		return r, tx.Commit()
	}
	// Fail closed on missing encryption key, rather than EncryptFields' dev no-op.
	token, secret := p.AccessToken, p.AppSecret
	if crypto.EncryptFields(c.App.EncryptionKey, &token, &secret) != nil || !strings.HasPrefix(token, "enc:") || !strings.HasPrefix(secret, "enc:") {
		return r, refused
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO whatsapp_accounts(id,organization_id,name,app_id,phone_id,business_id,access_token,app_secret,webhook_verify_token,api_version,status,is_default_incoming,is_default_outgoing,auto_read_receipt,business_calling_enabled,is_smb,pin,created_by_id,updated_by_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'inactive',false,false,false,false,false,'',$11,$11,$12,$12)`, p.AccountID, p.OrganizationID, p.Name, p.AppID, p.PhoneID, p.BusinessID, token, secret, p.VerifyToken, p.APIVersion, p.OperatorID, time.Now().UTC())
	if e != nil {
		return r, refused
	}
	changes, _ := json.Marshal([]map[string]string{{"field": "status", "new": "inactive"}, {"field": "operation", "new": "offline_carrier_account_provision"}})
	_, e = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at) VALUES($1,$2,'whatsapp_account',$3,$4,$5,'create',$6,$7)`, uuid.NewString(), p.OrganizationID, p.AccountID, p.OperatorID, operatorName, string(changes), time.Now().UTC())
	if e != nil {
		return r, refused
	}
	if e = tx.Commit(); e != nil {
		return r, refused
	}
	r.Applied = true
	return r, nil
}
