package main

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"strings"
	"testing"
)

func portablePlan(t *testing.T) Plan {
	t.Helper()
	oldR, oldC := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity = oldR; protectedschema.ExpectedCatalogSHA256 = oldC })
	p := Plan{Version: 1, InstanceID: uuid.NewString(), OrganizationID: uuid.NewString(), OperatorID: uuid.NewString(), Purpose: "synthetic-role-acceptance", AttemptNonce: strings.Repeat("a", 32), Generation: 5, Release: strings.Repeat("b", 40) + ":" + strings.Repeat("c", 40), CatalogSHA256: strings.Repeat("d", 64)}
	protectedschema.ReleaseIdentity = p.Release
	protectedschema.ExpectedCatalogSHA256 = p.CatalogSHA256
	for _, label := range fixtureLabels {
		p.Identities = append(p.Identities, Identity{Fixture: label, UserID: uuid.NewString(), MembershipID: uuid.NewString(), Email: "synthetic-role-" + label + "-1234abcd@example.invalid"})
	}
	return p
}
func TestStrictSixFixturePlan(t *testing.T) {
	p := portablePlan(t)
	raw, _ := json.Marshal(p)
	if _, e := decodePlan(raw); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"purpose":"synthetic-role-acceptance"`, `"purpose":"synthetic-role-acceptance","purpose":"synthetic-role-acceptance"`, 1), strings.Replace(string(raw), `"fixture":"employee"`, `"fixture":"employee","fixture":"employee"`, 1), strings.Replace(string(raw), `"fixture":"employee"`, `"fixture":"employee","is_super_admin":true`, 1), string(raw) + `{}`, strings.Replace(string(raw), "@example.invalid", "@example.com", 1)} {
		if _, e := decodePlan([]byte(bad)); e == nil {
			t.Fatal("unsafe plan accepted")
		}
	}
}
func TestPlanCannotChangePurposeOrLabelsOrExistingIdentity(t *testing.T) {
	original := portablePlan(t)
	for _, kind := range []string{"purpose", "fixture", "duplicate", "missing", "operator", "release", "catalog"} {
		p := original
		p.Identities = append([]Identity(nil), original.Identities...)
		switch kind {
		case "purpose":
			p.Purpose = "employee-enrollment"
		case "fixture":
			p.Identities[0].Fixture = "administrator"
		case "duplicate":
			p.Identities[1].UserID = p.Identities[0].UserID
		case "missing":
			p.Identities = p.Identities[:5]
		case "operator":
			p.Identities[0].UserID = p.OperatorID
		case "release":
			p.Release = strings.Repeat("f", 40) + ":" + strings.Repeat("e", 40)
		case "catalog":
			p.CatalogSHA256 = strings.Repeat("e", 64)
		}
		if validPlan(p) == nil {
			t.Fatal(kind)
		}
	}
}
