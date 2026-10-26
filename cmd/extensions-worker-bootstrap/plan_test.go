package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixturePlan() Plan {
	return Plan{Version: 1, Nonce: "11111111-1111-4111-8111-111111111111", InstanceID: "22222222-2222-4222-8222-222222222222", OrganizationID: "33333333-3333-4333-8333-333333333333", OperatorID: "44444444-4444-4444-8444-444444444444", WorkerID: "55555555-5555-4555-8555-555555555555", RoleID: "66666666-6666-4666-8666-666666666666", MembershipID: "77777777-7777-4777-8777-777777777777", PermissionID: "88888888-8888-4888-8888-888888888888", AuthStamp: "99999999-9999-4999-8999-999999999999", Email: "worker-55555555-5555-4555-8555-555555555555@example.invalid", DisplayName: "Knowledge processing service", Profile: "processing-worker-v1", Audiences: []string{"staff"}, ApprovalReference: "review/example", Reason: "Explicit service bootstrap", Generation: 3, Release: strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40), CatalogSHA256: strings.Repeat("c", 64), FunctionSHA256: strings.Repeat("d", 64), BeforeACL: Observation{Version: 1, Records: []ACLRecord{{Kind: "namespace", Object: "public", Owner: "owner", Explicit: true, Grants: []ACLGrant{}}}}}
}

func TestPlanBoundaries(t *testing.T) {
	p := fixturePlan()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*Plan)
	}{
		{"human_email", func(p *Plan) { p.Email = "person@example.com" }},
		{"wildcard", func(p *Plan) { p.Audiences = []string{"*"} }},
		{"duplicate_scope", func(p *Plan) { p.Audiences = []string{"staff", "staff"} }},
		{"identity_collision", func(p *Plan) { p.WorkerID = p.OperatorID }},
		{"nonzero_version", func(p *Plan) { p.ExpectedVersion = 1 }},
		{"missing_approval_provenance", func(p *Plan) { p.ApprovalReference = "" }},
		{"unbounded_generation", func(p *Plan) { p.Generation = 0 }},
		{"unsorted_observation", func(p *Plan) { p.BeforeACL.Records = append(p.BeforeACL.Records, ACLRecord{Kind: "a", Object: "a"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := fixturePlan()
			tc.edit(&q)
			if q.Validate() == nil {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
}

func TestStrictPlanRejectsCredentialsAndTrailingValues(t *testing.T) {
	b, _ := json.Marshal(fixturePlan())
	for _, s := range []string{strings.TrimSuffix(string(b), "}") + `,"password":"do-not-accept"}`, string(b) + ` {}`} {
		var p Plan
		if decode([]byte(s), &p) == nil {
			t.Fatal("unexpected input accepted")
		}
	}
}

func TestMetadataSQLOnlyTwelveReviewedColumns(t *testing.T) {
	p := fixturePlan()
	statements := metadataSQL(p)
	if len(statements) != 6 {
		t.Fatal("six statements required")
	}
	if !strings.Contains(statements[0], "generation,release,catalog_sha256") {
		t.Fatal("ORDER BY column missing")
	}
	for _, s := range statements {
		if strings.Contains(s, "SELECT *") || strings.Contains(s, "UPDATE") || strings.Contains(s, "WITH GRANT OPTION") {
			t.Fatal("broadened metadata privilege")
		}
	}
}

func TestExpectedACLRejectsUnrelatedChange(t *testing.T) {
	p := fixturePlan()
	before := aclFixture(p)
	after, err := expectedACL(before, p)
	if err != nil {
		t.Fatal(err)
	}
	if observationHash(before) == observationHash(after) {
		t.Fatal("delta absent")
	}
	for _, r := range after.Records {
		if r.Kind == "relation" && r.Object == "public.unrelated" && r.Owner != "unchanged" {
			t.Fatal("unrelated owner changed")
		}
	}
	changed := after
	changed.Records = append([]ACLRecord(nil), after.Records...)
	changed.Records[0].Owner = "intruder"
	if observationsEqual(changed, after) {
		t.Fatal("unrelated mutation ignored")
	}
}

func aclFixture(p Plan) Observation {
	database, owner, worker, _ := p.names()
	o := Observation{Version: 1}
	seen := map[string]bool{}
	add := func(r ACLRecord) {
		k := recordKey(r)
		if !seen[k] {
			seen[k] = true
			o.Records = append(o.Records, r)
		}
	}
	add(ACLRecord{Kind: "database", Object: database, Owner: owner, Explicit: true, Grants: []ACLGrant{{owner, owner, "CONNECT", false}}})
	add(ACLRecord{Kind: "namespace", Object: "public", Owner: owner, Explicit: true, Grants: []ACLGrant{{owner, owner, "CREATE", false}, {owner, owner, "USAGE", false}}})
	add(ACLRecord{Kind: "relation", Object: "public.er_processing_authorization_bindings", Owner: owner, Explicit: true})
	add(ACLRecord{Kind: "relation", Object: "public.unrelated", Owner: "unchanged"})
	add(ACLRecord{Kind: "routine", Object: signature, Owner: owner, Explicit: true, Grants: []ACLGrant{{owner, owner, "EXECUTE", false}}})
	add(ACLRecord{Kind: "role", Object: worker})
	for _, group := range [][]columnGrant{metadata, ownerColumns} {
		for _, g := range group {
			for _, c := range g.Select {
				add(ACLRecord{Kind: "column", Object: "public." + g.Table + "." + c, Owner: owner})
			}
		}
	}
	for _, c := range append(append([]string{}, runtimeJobSelect...), runtimeJobUpdate...) {
		add(ACLRecord{Kind: "column", Object: "public.addon_knowledge_jobs." + c, Owner: owner})
	}
	normalize(&o)
	return o
}
