package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

func fixture(t *testing.T) (*sql.DB, *config.Config, Plan, string, string) {
	t.Helper()
	dsn := os.Getenv("EXTENSION_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("disposable EXTENSION_ADMIN_TEST_DSN required")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "carrier_account_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec("CREATE SCHEMA " + schema); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	_, e = db.Exec(`CREATE TABLE organizations(id uuid PRIMARY KEY,deleted_at timestamptz);
 CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid,is_active boolean,deleted_at timestamptz,full_name text);
 CREATE TABLE whatomate_protection_state(id integer PRIMARY KEY,protected boolean);
 CREATE TABLE whatomate_schema_seal(generation bigserial PRIMARY KEY,release text,catalog_sha256 text);
 CREATE TABLE whatomate_schema_attempt(release text,catalog_sha256 text);
 CREATE TABLE whatsapp_accounts(id uuid PRIMARY KEY,organization_id uuid,name varchar(100),app_id varchar(100),phone_id varchar(100) UNIQUE,business_id varchar(100),access_token text,app_secret varchar(255),webhook_verify_token varchar(255),api_version varchar(20),status varchar(20),is_default_incoming boolean,is_default_outgoing boolean,auto_read_receipt boolean,business_calling_enabled boolean,is_smb boolean,pin varchar(255),created_by_id uuid,updated_by_id uuid,created_at timestamptz,updated_at timestamptz,deleted_at timestamptz,UNIQUE(organization_id,name));
 CREATE TABLE audit_logs(id uuid PRIMARY KEY,organization_id uuid,resource_type varchar(50),resource_id uuid,user_id uuid,user_name varchar(255),action varchar(20),changes jsonb,created_at timestamptz)`)
	if e != nil {
		t.Fatal(e)
	}
	p := Plan{SchemaVersion: 1, OrganizationID: uuid.NewString(), AccountID: uuid.NewString(), OperatorID: uuid.NewString(), Name: "Fixture", AppID: "123", PhoneID: "456", BusinessID: "789", APIVersion: "v26.0", AccessToken: "synthetic-access-value", AppSecret: "synthetic-app-secret", VerifyToken: "synthetic-verify-value"}
	_, e = db.Exec(`INSERT INTO organizations VALUES($1,NULL)`, p.OrganizationID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO whatomate_protection_state VALUES(1,true)`); e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO users VALUES($1,$2,true,NULL,'Fixture Owner')`, p.OperatorID, p.OrganizationID)
	if e != nil {
		t.Fatal(e)
	}
	oldR, oldS := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity = oldR; protectedschema.ExpectedCatalogSHA256 = oldS })
	protectedschema.ReleaseIdentity = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	shape, e := protectedschema.Catalog(context.Background(), db, schema)
	if e != nil {
		t.Fatal(e)
	}
	protectedschema.ExpectedCatalogSHA256 = shape.SHA256
	_, e = db.Exec(`INSERT INTO whatomate_schema_seal(release,catalog_sha256) VALUES($1,$2)`, protectedschema.ReleaseIdentity, shape.SHA256)
	if e != nil {
		t.Fatal(e)
	}
	c := &config.Config{}
	c.Extensions.ProtectedMode = true
	c.App.EncryptionKey = "synthetic-key-32-characters-long-enough"
	c.Database.SchemaManagedExternally = true
	c.Database.SchemaRelease = protectedschema.ReleaseIdentity
	c.Database.SchemaSHA256 = shape.SHA256
	if e = db.QueryRow(`SELECT current_database(),current_user`).Scan(&c.Database.Name, &c.Database.User); e != nil {
		t.Fatal(e)
	}
	dir, e := os.MkdirTemp("/root", "carrier-account-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return db, c, p, schema, filepath.Join(dir, "attempt.json")
}

func TestPreviewAndEncryptedInactiveCreate(t *testing.T) {
	db, c, p, s, a := fixture(t)
	ctx := context.Background()
	r, e := provision(ctx, db, c, p, s, a, false)
	if e != nil || r.Applied {
		t.Fatal("preview", e)
	}
	if _, e = os.Stat(a); !os.IsNotExist(e) {
		t.Fatal("preview wrote attempt")
	}
	r, e = provision(ctx, db, c, p, s, a, true)
	if e != nil || !r.Applied {
		t.Fatal("apply", e)
	}
	var token, secret, verify, status string
	var unsafe bool
	e = db.QueryRow(`SELECT access_token,app_secret,webhook_verify_token,status,is_default_incoming OR is_default_outgoing OR auto_read_receipt OR business_calling_enabled OR is_smb FROM whatsapp_accounts WHERE id=$1`, p.AccountID).Scan(&token, &secret, &verify, &status, &unsafe)
	if e != nil || status != "inactive" || unsafe || !strings.HasPrefix(token, "enc:") || !strings.HasPrefix(secret, "enc:") || verify != p.VerifyToken {
		t.Fatal("stored account boundary", e)
	}
	plain, e := crypto.Decrypt(token, c.App.EncryptionKey)
	if e != nil || plain != p.AccessToken {
		t.Fatal("token cipher compatibility")
	}
	plain, e = crypto.Decrypt(secret, c.App.EncryptionKey)
	if e != nil || plain != p.AppSecret {
		t.Fatal("secret cipher compatibility")
	}
	if _, e = provision(ctx, db, c, p, s, a+"-new", true); e == nil {
		t.Fatal("existing account accepted")
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM audit_logs").Scan(&n); e != nil || n != 1 {
		t.Fatal("atomic audit", e)
	}
}

func TestRefusedBoundariesAndAuditRollback(t *testing.T) {
	for _, kind := range []string{"foreign-operator", "missing-org", "seal", "pending", "audit", "lease", "existing-attempt"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, s, a := fixture(t)
			ctx := context.Background()
			switch kind {
			case "foreign-operator":
				db.Exec(`UPDATE users SET organization_id=$1`, uuid.NewString())
			case "missing-org":
				db.Exec(`DELETE FROM organizations`)
			case "seal":
				db.Exec(`INSERT INTO whatomate_schema_seal(release,catalog_sha256) VALUES('wrong','wrong')`)
			case "pending":
				db.Exec(`INSERT INTO whatomate_schema_attempt VALUES('pending','pending')`)
			case "audit":
				db.Exec(`ALTER TABLE audit_logs ADD CONSTRAINT deny_write CHECK(false)`)
				shape, e := protectedschema.Catalog(ctx, db, s)
				if e != nil {
					t.Fatal(e)
				}
				protectedschema.ExpectedCatalogSHA256 = shape.SHA256
				c.Database.SchemaSHA256 = shape.SHA256
				db.Exec(`UPDATE whatomate_schema_seal SET catalog_sha256=$1`, shape.SHA256)
			case "lease":
				conn, e := db.Conn(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer conn.Close()
				if _, e = conn.ExecContext(ctx, `SELECT pg_advisory_lock_shared($1)`, protectedschema.LockID); e != nil {
					t.Fatal(e)
				}
				defer conn.ExecContext(ctx, `SELECT pg_advisory_unlock_shared($1)`, protectedschema.LockID)
			case "existing-attempt":
				os.WriteFile(a, []byte("retain"), 0600)
			}
			if kind == "audit" {
				if _, e := provision(ctx, db, c, p, s, a, false); e != nil {
					t.Fatal("audit fixture failed before write boundary", e)
				}
			}
			if _, e := provision(ctx, db, c, p, s, a, true); e == nil {
				t.Fatal("unsafe provision accepted")
			}
			var n int
			db.QueryRow(`SELECT count(*) FROM whatsapp_accounts`).Scan(&n)
			if n != 0 {
				t.Fatal("account escaped refusal")
			}
		})
	}
}

func TestReservedAndDeletedAccountIdentitiesNeverReused(t *testing.T) {
	for _, kind := range []string{"phone", "business", "org-name", "deleted-uuid"} {
		t.Run(kind, func(t *testing.T) {
			db, c, p, s, a := fixture(t)
			id, org, name, phone, business := uuid.NewString(), uuid.NewString(), "Existing", "9991", "9992"
			switch kind {
			case "phone":
				phone = p.PhoneID
			case "business":
				business = p.BusinessID
			case "org-name":
				org = p.OrganizationID
				name = p.Name
			case "deleted-uuid":
				id = p.AccountID
			}
			_, e := db.Exec(`INSERT INTO whatsapp_accounts(id,organization_id,name,phone_id,business_id,status,is_default_incoming,is_default_outgoing,deleted_at) VALUES($1,$2,$3,$4,$5,'active',true,true,now())`, id, org, name, phone, business)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = provision(context.Background(), db, c, p, s, a, true); e == nil {
				t.Fatal("reserved identity reused")
			}
			var n int
			var incoming, outgoing bool
			db.QueryRow(`SELECT count(*) FROM whatsapp_accounts`).Scan(&n)
			db.QueryRow(`SELECT is_default_incoming,is_default_outgoing FROM whatsapp_accounts WHERE id=$1`, id).Scan(&incoming, &outgoing)
			if n != 1 || !incoming || !outgoing {
				t.Fatal("existing defaults changed")
			}
		})
	}
}

func TestConcurrentCreateHasOneAccountAndOneAudit(t *testing.T) {
	db, c, p, s, a := fixture(t)
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := provision(context.Background(), db, c, p, s, a+string(rune('a'+i)), true)
			out <- e
		}(i)
	}
	wg.Wait()
	close(out)
	success := 0
	for e := range out {
		if e == nil {
			success++
		}
	}
	var accounts, audits int
	db.QueryRow(`SELECT count(*) FROM whatsapp_accounts`).Scan(&accounts)
	db.QueryRow(`SELECT count(*) FROM audit_logs`).Scan(&audits)
	if success != 1 || accounts != 1 || audits != 1 {
		t.Fatal("concurrent create did not preserve one-shot atomicity", success, accounts, audits)
	}
}

func TestPrivateInputAndSanitizedOutput(t *testing.T) {
	dir, e := os.MkdirTemp("/root", "carrier-account-input-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "input")
	os.WriteFile(path, []byte("private"), 0600)
	if _, e = readPrivate(path); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	os.Symlink(path, link)
	if _, e = readPrivate(link); e == nil {
		t.Fatal("symlink accepted")
	}
	os.Chmod(path, 0644)
	if _, e = readPrivate(path); e == nil {
		t.Fatal("public input accepted")
	}
	os.Chmod(path, 0600)
	os.Chmod(dir, 0777)
	if _, e = readPrivate(path); e == nil {
		t.Fatal("writable ancestor accepted")
	}
	os.Chmod(dir, 0700)
	os.Setenv("WHATOMATE_APP_ENCRYPTION_KEY", "untrusted-env-value")
	defer os.Unsetenv("WHATOMATE_APP_ENCRYPTION_KEY")
	os.WriteFile(path, []byte("[app]\nencryption_key='file-value'\n"), 0600)
	c, e := readConfig(path)
	if e != nil || c.App.EncryptionKey != "file-value" {
		t.Fatal("file-only config")
	}
	if _, e = decodePlan([]byte(`{"schema_version":1,"unexpected":"deny"}`)); e == nil {
		t.Fatal("unknown plan field accepted")
	}
	_, _, p, _, _ := fixture(t)
	raw, _ := json.Marshal(p)
	duplicate := append([]byte(`{"schema_version":1,`), raw[1:]...)
	if _, e = decodePlan(duplicate); e == nil {
		t.Fatal("duplicate valid plan field accepted")
	}
	output, _ := json.Marshal(Result{OrganizationID: p.OrganizationID, AccountID: p.AccountID, Status: "inactive", Applied: true})
	for _, secret := range []string{p.AccessToken, p.AppSecret, p.VerifyToken} {
		if strings.Contains(string(output), secret) || strings.Contains(refused.Error(), secret) {
			t.Fatal("secret escaped sanitized output")
		}
	}
}

func TestCatalogTamperDeniesEvenMatchingStoredSeal(t *testing.T) {
	db, c, p, s, a := fixture(t)
	if _, e := db.Exec(`CREATE INDEX unexpected_account_index ON whatsapp_accounts(name)`); e != nil {
		t.Fatal(e)
	}
	if _, e := provision(context.Background(), db, c, p, s, a, true); e == nil {
		t.Fatal("tampered physical catalog accepted")
	}
}
