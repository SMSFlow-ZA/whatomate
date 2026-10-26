package main

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresACLObservationPreservesLongObjectNames(t *testing.T) {
	db := disposable(t)
	prepareFixture(t, db)
	name := "fixture_long_acl_index_" + strings.Repeat("x", 40)
	if len(name) != 63 {
		t.Fatal("fixture must use exact PostgreSQL identifier limit")
	}
	if _, e := db.Exec(`CREATE TABLE public.fixture_long_acl_table(a integer,b integer,c integer); CREATE INDEX ` + name + ` ON public.fixture_long_acl_table(a,b,c)`); e != nil {
		t.Fatal(e)
	}
	var kind string
	if e := db.QueryRow(`WITH observed AS (` + ACLObservationSQL + `) SELECT pg_typeof(object)::text FROM observed LIMIT 1`).Scan(&kind); e != nil || kind != "text" {
		t.Fatalf("object projection must be text, got %s: %v", kind, e)
	}
	observed, e := captureACL(context.Background(), db)
	if e != nil {
		t.Fatal("complete long-name observation refused", e)
	}
	want := map[string]bool{}
	for _, column := range []string{"a", "b", "c"} {
		want["public."+name+"."+column] = false
	}
	for _, r := range observed.Records {
		if _, ok := want[r.Object]; ok && r.Kind == "column" {
			want[r.Object] = true
		}
	}
	for key, found := range want {
		if !found || len(key) <= 63 {
			t.Fatalf("full distinct column key lost: %s", key)
		}
	}
}
