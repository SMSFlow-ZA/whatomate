package handlers

import (
	"context"
	"database/sql"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/google/uuid"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConversationCitationsPersistQuestionOnlyReply(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	base, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	ns := "citation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = base.Exec(`CREATE SCHEMA ` + ns); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", ns)
	u.RawQuery = q.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); base.Exec(`DROP SCHEMA ` + ns + ` CASCADE`); base.Close() })
	for _, f := range []func(context.Context) error{(&cases.SQLRepository{DB: db}).Migrate, func(ctx context.Context) error { return (addonapi.AccessStore{DB: db}).Migrate(ctx) }, func(ctx context.Context) error { return conversation.Migrate(ctx, db) }} {
		if e = f(ctx); e != nil {
			t.Fatal(e)
		}
	}
	tenant := "synthetic-citation-" + uuid.NewString()
	store, e := directory.NewPostgresStore(db, tenant, "synthetic-fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Init(ctx); e != nil {
		t.Fatal(e)
	}
	preview, e := directory.NewPreview([]directory.Employee{{ID: "synthetic-employee", Phone: "+27820000001", Zone: "synthetic-unit", Department: "synthetic-department", Active: true}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = directory.Apply(ctx, store, preview, preview.Digest(), time.Now().Add(-time.Second)); e != nil {
		t.Fatal(e)
	}
	answer := citationFixture()
	service := conversation.Service{DB: db, Config: conversation.HTTPConfig{Clock: time.Now, Freshness: time.Hour, Aliases: (addonapi.AccessStore{DB: db}).EmployeeAliases, Advisor: func(context.Context, addonapi.Actor, string) (conversation.Answer, error) {
		return extensionConversationAnswer(answer), nil
	}}}
	inbound := conversation.Inbound{Tenant: tenant, Account: "synthetic", Sender: "+27820000001", ProviderMessageID: "synthetic-question-1", Text: "designated reviewer"}
	turn, e := service.Receive(ctx, inbound)
	if e != nil {
		t.Fatal(e)
	}
	if turn.Selected != "" || len(turn.Cases) != 0 || !strings.Contains(turn.Reply, "Sources:") || !strings.Contains(turn.Reply, answer.Citations[0].Reference) {
		t.Fatal("question-only reply lost citations or invented a case")
	}
	messages, e := delivery.List(ctx, db, tenant, inbound.Sender)
	if e != nil || len(messages) != 1 || messages[0].Text != turn.Reply || messages[0].CaseID != "" {
		t.Fatal("durable outgoing text lost source provenance", e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM addon_cases_case WHERE organization_id=$1`, tenant).Scan(&count); e != nil || count != 0 {
		t.Fatal("question created an unconfirmed case", e)
	}
	replay, e := service.Receive(ctx, inbound)
	if e != nil || replay.Reply != turn.Reply {
		t.Fatal("replay changed sourced response", e)
	}
	messages, e = delivery.List(ctx, db, tenant, inbound.Sender)
	if e != nil || len(messages) != 1 {
		t.Fatal("duplicate question queued duplicate delivery", e)
	}
}
