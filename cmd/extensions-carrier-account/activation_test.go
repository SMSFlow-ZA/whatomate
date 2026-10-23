package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

func TestActivationPreviewAndExactStatusOnlyApply(t *testing.T) {
	db, c, p, s, a := fixture(t)
	if _, err := provision(context.Background(), db, c, p, s, a, true); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT row_to_json(w)::text FROM whatsapp_accounts w WHERE id=$1`, p.AccountID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	r, err := activate(context.Background(), db, c, p, s, a+"-activation", false)
	if err != nil || r.Applied || r.Status != "active" {
		t.Fatal("activation preview", err)
	}
	if _, err = os.Stat(a + "-activation"); !os.IsNotExist(err) {
		t.Fatal("preview wrote attempt")
	}
	r, err = activate(context.Background(), db, c, p, s, a+"-activation", true)
	if err != nil || !r.Applied || r.Status != "active" {
		t.Fatal("activation", err)
	}
	var unchanged bool
	if err = db.QueryRow(`SELECT status='active' AND (to_jsonb(w)-'status'-'updated_by_id'-'updated_at')=($2::jsonb-'status'-'updated_by_id'-'updated_at') FROM whatsapp_accounts w WHERE id=$1`, p.AccountID, before).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("activation changed unrelated fields", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='update'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("activation audit", err)
	}
	if _, err = activate(context.Background(), db, c, p, s, a+"-replay", true); err == nil {
		t.Fatal("active replay accepted")
	}
}

func TestActivationExactIdentityCredentialsAndStatusRefusal(t *testing.T) {
	for _, kind := range []string{"account", "organization", "operator", "name", "app", "phone", "business", "api", "access-token", "app-secret", "verify-token", "stored-plaintext-token", "stored-corrupt-token", "stored-corrupt-secret", "deleted", "active", "foreign-operator", "inactive-operator", "deleted-operator", "wrong-key", "existing-attempt"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, s, a := fixture(t)
			if _, err := provision(context.Background(), db, c, p, s, a, true); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "account":
				p.AccountID = uuid.NewString()
			case "organization":
				p.OrganizationID = uuid.NewString()
			case "operator":
				p.OperatorID = uuid.NewString()
			case "name":
				p.Name = "Wrong"
			case "app":
				p.AppID = "1234"
			case "phone":
				p.PhoneID = "4567"
			case "business":
				p.BusinessID = "7891"
			case "api":
				p.APIVersion = "v25.0"
			case "access-token":
				p.AccessToken = "synthetic-wrong-token"
			case "app-secret":
				p.AppSecret = "synthetic-wrong-secret"
			case "verify-token":
				p.VerifyToken = "synthetic-wrong-verification"
			case "stored-plaintext-token":
				db.Exec(`UPDATE whatsapp_accounts SET access_token=$1`, p.AccessToken)
			case "stored-corrupt-token":
				db.Exec(`UPDATE whatsapp_accounts SET access_token='enc:invalid'`)
			case "stored-corrupt-secret":
				db.Exec(`UPDATE whatsapp_accounts SET app_secret='enc:invalid'`)
			case "deleted":
				db.Exec(`UPDATE whatsapp_accounts SET deleted_at=now()`)
			case "active":
				db.Exec(`UPDATE whatsapp_accounts SET status='active'`)
			case "foreign-operator":
				db.Exec(`UPDATE users SET organization_id=$1`, uuid.NewString())
			case "inactive-operator":
				db.Exec(`UPDATE users SET is_active=false`)
			case "deleted-operator":
				db.Exec(`UPDATE users SET deleted_at=now()`)
			case "wrong-key":
				c.App.EncryptionKey = "another-synthetic-key-32-bytes-long"
			case "existing-attempt":
				os.WriteFile(a+"-activate", []byte("preserve unknown outcome"), 0600)
			}
			if _, err := activate(context.Background(), db, c, p, s, a+"-activate", true); err == nil {
				t.Fatal("unsafe activation accepted")
			}
			var updates int
			if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='update'`).Scan(&updates); err != nil || updates != 0 {
				t.Fatal("activation escaped refusal", err)
			}
			if _, err := os.Stat(a + "-activate"); err != nil {
				t.Fatal("attempt not retained", err)
			}
		})
	}
}

func TestActivationFenceSchemaAndAtomicAuditRollback(t *testing.T) {
	for _, kind := range []string{"runtime", "seal", "pending", "protection", "catalog", "audit", "wrong-owner"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, s, a := fixture(t)
			ctx := context.Background()
			if _, err := provision(ctx, db, c, p, s, a, true); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "runtime":
				conn, err := db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock_shared($1)`, protectedschema.LockID); err != nil {
					t.Fatal(err)
				}
				defer conn.ExecContext(ctx, `SELECT pg_advisory_unlock_shared($1)`, protectedschema.LockID)
			case "seal":
				db.Exec(`INSERT INTO whatomate_schema_seal(release,catalog_sha256) VALUES('wrong','wrong')`)
			case "pending":
				db.Exec(`INSERT INTO whatomate_schema_attempt VALUES('pending','pending')`)
			case "protection":
				db.Exec(`UPDATE whatomate_protection_state SET protected=false`)
			case "catalog":
				db.Exec(`CREATE INDEX unexpected_activation_index ON whatsapp_accounts(name)`)
			case "wrong-owner":
				c.Database.User = "not_the_connected_owner"
			case "audit":
				if _, err := db.Exec(`ALTER TABLE audit_logs ADD CONSTRAINT deny_activation CHECK(action<>'update')`); err != nil {
					t.Fatal(err)
				}
				shape, err := protectedschema.Catalog(ctx, db, s)
				if err != nil {
					t.Fatal(err)
				}
				protectedschema.ExpectedCatalogSHA256 = shape.SHA256
				c.Database.SchemaSHA256 = shape.SHA256
				db.Exec(`UPDATE whatomate_schema_seal SET catalog_sha256=$1`, shape.SHA256)
				if _, err = activate(ctx, db, c, p, s, a+"-preview", false); err != nil {
					t.Fatal("audit fixture failed before write boundary", err)
				}
			}
			if _, err := activate(ctx, db, c, p, s, a+"-activate", true); err == nil {
				t.Fatal("unsafe activation accepted")
			}
			var status string
			var audits int
			db.QueryRow(`SELECT status FROM whatsapp_accounts WHERE id=$1`, p.AccountID).Scan(&status)
			db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='update'`).Scan(&audits)
			if status != "inactive" || audits != 0 {
				t.Fatal("atomic rollback failed", status, audits)
			}
			if kind == "runtime" {
				if _, err := activate(ctx, db, c, p, s, a+"-readonly", false); err == nil {
					t.Fatal("preview accepted live runtime")
				}
			}
		})
	}
}

func TestConcurrentActivationHasOneStatusChangeAndSanitizedAudit(t *testing.T) {
	db, c, p, s, a := fixture(t)
	ctx := context.Background()
	if _, err := provision(ctx, db, c, p, s, a, true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := activate(ctx, db, c, p, s, a+string(rune('x'+i)), true)
			out <- err
		}(i)
	}
	wg.Wait()
	close(out)
	success := 0
	for err := range out {
		if err == nil {
			success++
		}
	}
	var updates int
	var status, changes string
	db.QueryRow(`SELECT status FROM whatsapp_accounts WHERE id=$1`, p.AccountID).Scan(&status)
	db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='update'`).Scan(&updates)
	if success != 1 || updates != 1 || status != "active" {
		t.Fatal("activation concurrency", success, updates, status)
	}
	if err := db.QueryRow(`SELECT changes::text FROM audit_logs WHERE action='update'`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Result{OrganizationID: p.OrganizationID, AccountID: p.AccountID, Status: "active", Applied: true})
	for _, value := range []string{p.AccessToken, p.AppSecret, p.VerifyToken, p.Name, p.PhoneID, p.BusinessID, p.AppID} {
		if strings.Contains(changes, value) {
			t.Fatal("private plan material escaped audit/output")
		}
	}
	// Short synthetic phone/app IDs can occur by chance inside an unrelated UUID;
	// verify the result's exact key set, and inspect long credential values too.
	var output map[string]any
	if json.Unmarshal(raw, &output) != nil || len(output) != 5 || output["account_id"] != p.AccountID || output["organization_id"] != p.OrganizationID || output["status"] != "active" || output["applied"] != true || output["provider_calls"] != false {
		t.Fatal("activation result contract changed")
	}
	for _, value := range []string{p.AccessToken, p.AppSecret, p.VerifyToken} {
		if strings.Contains(string(raw), value) {
			t.Fatal("credential escaped result")
		}
	}
}
