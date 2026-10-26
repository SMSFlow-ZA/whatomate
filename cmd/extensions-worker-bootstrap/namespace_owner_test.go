package main

import (
	"context"
	"testing"
)

func TestPostgresPGDatabaseOwnerNamespaceUsesActualGrantor(t *testing.T) {
	db := disposable(t)
	p := prepareFixture(t, db)
	if _, e := db.Exec(`ALTER SCHEMA public OWNER TO pg_database_owner`); e != nil {
		t.Fatal(e)
	}
	refreshPins(t, db, &p)
	s := fixtureSession(t, db, p)
	for i := range p.BeforeACL.Records {
		r := &p.BeforeACL.Records[i]
		if r.Kind == "namespace" && r.Object == "public" {
			old := r.Owner
			r.Owner = "unreviewed_owner"
			if _, e := expectedACL(p.BeforeACL, p); e == nil {
				t.Fatal("unreviewed namespace owner accepted")
			}
			r.Owner = old
		}
	}
	if r := s.Execute(context.Background(), p, "preview"); r.Outcome != "preview" {
		t.Fatalf("legitimate database-owner namespace refused: %s %s", r.Outcome, r.Code)
	}
	if r := s.Execute(context.Background(), p, "apply"); r.Outcome != "committed-observed" {
		t.Fatalf("namespace grantor readback mismatch: %s %s", r.Outcome, r.Code)
	}
	after, e := captureACL(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	_, _, _, owner := p.names()
	found := false
	for _, r := range after.Records {
		if r.Kind == "namespace" && r.Object == "public" {
			if r.Owner != "pg_database_owner" {
				t.Fatal("namespace ownership changed")
			}
			for _, g := range r.Grants {
				if g.Grantee == owner && g.Privilege == "USAGE" {
					found = true
					if g.Grantor != "pg_database_owner" {
						t.Fatal("grantor not actual schema owner")
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("bounded schema usage grant missing")
	}
}
