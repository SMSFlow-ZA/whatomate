package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/zerodha/logf"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProtectedCarrierMissingSignatureFailsBeforePersistence(t *testing.T) {
	a := &App{ExtensionCarrier: &carrier.Service{Bindings: []carrier.Binding{{Tenant: "org", Account: "account", PhoneID: "phone", AppSecret: "secret"}}}}
	r := testutil.NewRequest(t)
	p := WebhookPayload{}
	handled, e := a.protectedCarrierWebhook(r, p, []byte(`{}`), nil)
	if e != nil {
		t.Fatal(e)
	}
	if !handled || r.RequestCtx.Response.StatusCode() != 403 {
		t.Fatal("unsigned protected ingress was not denied")
	}
}
func TestProtectedCarrierSignedHostCaseAndReceipt(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	base, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("carrier_host_%d", time.Now().UnixNano())
	if _, e = base.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	gdb, e := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	db, e := gdb.DB()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	if _, e = db.Exec(`CREATE TABLE whatsapp_accounts(id uuid PRIMARY KEY,organization_id uuid NOT NULL,phone_id text NOT NULL,business_id text NOT NULL,status text NOT NULL,app_secret text NOT NULL DEFAULT '',deleted_at timestamptz);CREATE TABLE contacts(id uuid);CREATE TABLE messages(id uuid)`); e != nil {
		t.Fatal(e)
	}
	org, account := uuid.NewString(), uuid.NewString()
	if _, e = db.Exec(`INSERT INTO whatsapp_accounts(id,organization_id,phone_id,business_id,status) VALUES($1,$2,'phone','business','active')`, account, org); e != nil {
		t.Fatal(e)
	}
	for _, fn := range []func(context.Context) error{(&cases.SQLRepository{DB: db}).Migrate, (&cases.SQLRepository{DB: db}).MigrateOutboxDelivery, func(c context.Context) error { return conversation.Migrate(c, db) }, func(c context.Context) error { return (addonapi.AccessStore{DB: db}).Migrate(c) }} {
		if e = fn(ctx); e != nil {
			t.Fatal(e)
		}
	}
	store, e := directory.NewPostgresStore(db, org, "fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Init(ctx); e != nil {
		t.Fatal(e)
	}
	preview, e := directory.NewPreview([]directory.Employee{{ID: "employee", Phone: "+27820000001", Zone: "zone", Department: "department", Active: true}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = directory.Apply(ctx, store, preview, preview.Digest(), time.Now().Add(-time.Second)); e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	a := &App{DB: gdb, Config: &config.Config{Extensions: config.ExtensionsConfig{CarrierEnabled: true, RequiredInbound: "directory", RosterFreshnessSeconds: 3600, CarrierBindings: []config.ExtensionCarrierBinding{{OrganizationID: org, AccountID: account, PhoneID: "phone", AppSecret: "synthetic-secret"}}}}, Log: logf.New(logf.Opts{Writer: &logs}), ExtensionGuard: ext.NewGuard(), ExtensionConversation: &conversation.Service{DB: db, Config: conversation.HTTPConfig{Clock: time.Now, Freshness: time.Hour, Aliases: (addonapi.AccessStore{DB: db}).EmployeeAliases}}}
	otherAccount := uuid.NewString()
	if _, e = db.Exec(`INSERT INTO whatsapp_accounts(id,organization_id,phone_id,business_id,status) VALUES($1,$2,'other-phone','business','active')`, otherAccount, org); e != nil {
		t.Fatal(e)
	}
	refusedConfig := *a.Config
	refused := &App{DB: a.DB, Config: &refusedConfig, ExtensionGuard: ext.NewGuard(), ExtensionConversation: a.ExtensionConversation}
	refused.Config.Extensions.CarrierBindings = append(append([]config.ExtensionCarrierBinding(nil), a.Config.Extensions.CarrierBindings...), config.ExtensionCarrierBinding{OrganizationID: org, AccountID: otherAccount, PhoneID: "other-phone", AppSecret: "secret"})
	refused.ExtensionGuard = ext.NewGuard()
	if e = refused.initializeProtectedCarrier(ctx, db); e == nil || refused.ExtensionCarrier != nil {
		t.Fatal("ambiguous acknowledgement account configuration accepted", e)
	}
	if e = a.initializeProtectedCarrier(ctx, db); e != nil {
		t.Fatal(e)
	}
	if a.ExtensionCarrier.SendObserved != nil {
		t.Fatal("dispatch disabled but live sender configured")
	}
	seed, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"synthetic-held", "disabled-queued"} {
		if e = delivery.Enqueue(ctx, seed, delivery.Message{ID: id, Tenant: org, Account: account, Sender: "+27820000001", EmployeeID: "employee", Text: "minimal synthetic notice"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = seed.Exec(`UPDATE addon_conversation_delivery SET state='held' WHERE id='synthetic-held'`); e != nil {
		t.Fatal(e)
	}
	if e = seed.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = a.ExtensionCarrier.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	var disabledState string
	if e = db.QueryRow(`SELECT state FROM addon_conversation_delivery WHERE id='disabled-queued'`).Scan(&disabledState); e != nil || disabledState != "held" {
		t.Fatal("dispatch-disabled intent was not held", disabledState, e)
	}

	calls := 0
	ack := ""
	a.ExtensionCarrier.SendObserved = func(_ context.Context, m delivery.Message) (delivery.Result, string, error) {
		calls++
		if m.CaseID != "" {
			ack = m.ID
		}
		return delivery.Accepted, "provider:" + m.ID, nil
	}
	post := func(body string, signed bool) int {
		t.Helper()
		r := testutil.NewRequest(t)
		r.RequestCtx.Request.SetBodyString(body)
		if signed {
			mac := hmac.New(sha256.New, []byte("synthetic-secret"))
			mac.Write([]byte(body))
			r.RequestCtx.Request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		if e = a.WebhookHandler(r); e != nil {
			t.Fatal(e)
		}
		return r.RequestCtx.Response.StatusCode()
	}
	if _, e = db.Exec(`UPDATE whatsapp_accounts SET app_secret='synthetic-secret' WHERE id=$1`, otherAccount); e != nil {
		t.Fatal(e)
	}
	ts := time.Now().Unix() - 1
	message := func(id, text string) string {
		return fmt.Sprintf(`{"entry":[{"id":"business","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"phone"},"messages":[{"id":%q,"from":"27820000001","timestamp":%q,"type":"text","text":{"body":%q}}]}}]}]}`, id, strconv.FormatInt(ts, 10), text)
	}
	genericBody := strings.Replace(message("generic", "ordinary synthetic message"), `"phone"`, `"other-phone"`, 1)
	bridge := func(body string) (bool, int) {
		t.Helper()
		var p WebhookPayload
		if e = json.Unmarshal([]byte(body), &p); e != nil {
			t.Fatal(e)
		}
		mac := hmac.New(sha256.New, []byte("synthetic-secret"))
		mac.Write([]byte(body))
		r := testutil.NewRequest(t)
		handled, e := a.protectedCarrierWebhook(r, p, []byte(body), []byte("sha256="+hex.EncodeToString(mac.Sum(nil))))
		if e != nil {
			t.Fatal(e)
		}
		return handled, r.RequestCtx.Response.StatusCode()
	}
	if _, e = db.Exec(`UPDATE whatsapp_accounts SET phone_id='changed-protected-phone',app_secret='synthetic-secret' WHERE id=$1`, account); e != nil {
		t.Fatal(e)
	}
	if handled, code := bridge(strings.Replace(message("changed-protected", "protected content"), `"phone"`, `"changed-protected-phone"`, 1)); !handled || code != 403 {
		t.Fatal("protected account phone change escaped to generic processing", handled, code)
	}
	if _, e = db.Exec(`UPDATE whatsapp_accounts SET phone_id='phone' WHERE id=$1`, account); e != nil {
		t.Fatal(e)
	}
	if handled, code := bridge(genericBody); handled {
		t.Fatal("known authenticated ordinary account did not retain legacy path", code)
	}
	var mixed, generic WebhookPayload
	if e = json.Unmarshal([]byte(message("mixed", "protected text")), &mixed); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal([]byte(genericBody), &generic); e != nil {
		t.Fatal(e)
	}
	mixed.Entry = append(mixed.Entry, generic.Entry...)
	mixedRaw, e := json.Marshal(mixed)
	if e != nil {
		t.Fatal(e)
	}
	if handled, code := bridge(string(mixedRaw)); !handled || code != 403 {
		t.Fatal("mixed protected/ordinary batch escaped", handled, code)
	}
	if code := post(message("unsigned", "private concern"), false); code != 403 {
		t.Fatal("unsigned", code)
	}
	if code := post(strings.Replace(message("wrong-business", "private concern"), `"business"`, `"other-business"`, 1), true); code != 403 {
		t.Fatal("wrong business accepted", code)
	}
	if code := post(strings.Replace(message("wrong-account", "private concern"), `"phone"`, `"unbound-phone"`, 1), true); code != 403 {
		t.Fatal("wrong account accepted", code)
	}
	for i, text := range []string{"log case", "A synthetic workplace concern.", "confirm"} {
		body := message(strconv.Itoa(i), text)
		if code := post(body, true); code != 200 {
			t.Fatalf("signed ingress %d: %d", i, code)
		}
		if code := post(body, true); code != 200 {
			t.Fatal("replay", code)
		}
		if e = a.ExtensionCarrier.Tick(ctx); e != nil {
			t.Fatal(e)
		}
	}
	var heldState string
	if e = db.QueryRow(`SELECT state FROM addon_conversation_delivery WHERE id='synthetic-held'`).Scan(&heldState); e != nil || heldState != "held" {
		t.Fatal("new binding resumed an existing held intent", heldState, e)
	}
	if calls != 3 || ack == "" {
		t.Fatal("unexpected sends or missing ack", calls, ack)
	}
	var casesCount, ackCount int
	if e = db.QueryRow(`SELECT count(*) FROM addon_cases_case`).Scan(&casesCount); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM addon_cases_outbox`).Scan(&ackCount); e != nil {
		t.Fatal(e)
	}
	if casesCount != 1 || ackCount != 1 {
		t.Fatal(casesCount, ackCount)
	}
	receipt := fmt.Sprintf(`{"entry":[{"id":"business","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"phone"},"statuses":[{"id":%q,"status":"delivered","timestamp":%q,"biz_opaque_callback_data":%q}]}}]}]}`, "provider:"+ack, strconv.FormatInt(ts, 10), ack)
	if code := post(receipt, true); code != 200 {
		t.Fatal(code)
	}
	if e = a.ExtensionCarrier.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = db.QueryRow(`SELECT state FROM addon_cases_delivery`).Scan(&state); e != nil || state != "delivered" {
		t.Fatal(state, e)
	}
	for _, table := range []string{"contacts", "messages"} {
		var n int
		if e = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 0 {
			t.Fatal("generic persistence", table, n, e)
		}
	}
	if strings.Contains(logs.String(), "27820000001") || strings.Contains(logs.String(), "workplace concern") {
		t.Fatal("protected data logged")
	}
}
func TestProtectedCarrierMalformedPayloadDoesNotLog(t *testing.T) {
	a := &App{Config: &config.Config{Extensions: config.ExtensionsConfig{CarrierEnabled: true}}}
	r := testutil.NewRequest(t)
	r.RequestCtx.Request.SetBodyString(`{"entry":"private invalid payload"`)
	if e := a.WebhookHandler(r); e != nil {
		t.Fatal(e)
	}
	if r.RequestCtx.Response.StatusCode() != 400 {
		t.Fatal("malformed protected payload accepted")
	}
}
