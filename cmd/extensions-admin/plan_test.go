package main

import (
	"strings"
	"testing"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/google/uuid"
)

func fixturePlan() (Policy, Plan) {
	org, operator, user := uuid.NewString(), uuid.NewString(), uuid.NewString()
	version, role := int64(0), ""
	return Policy{OrganizationID: org, OperatorID: operator, NonemployeeOperators: map[string]string{user: "reviewed-external-operator-17"}, Profiles: map[string]Profile{"triage": {Kind: "operator", SubjectKind: "nonemployee", UserIDs: []string{user}, Permissions: []string{"security:mfa.enroll", "cases:read", "cases:read.triage"}, Grants: []api.Grant{{Permission: "cases:read.triage", UnitID: "north"}}}}}, Plan{OrganizationID: org, UserID: user, Action: "replace", Profile: "triage", ExpectedVersion: &version, ExpectedRoleID: &role, Reason: "change-reference-17"}
}

func TestPlanRejectsEscalationAndUnpinnedSubjects(t *testing.T) {
	for _, mutate := range []func(*Policy, *Plan){
		func(_ *Policy, p *Plan) { p.OrganizationID = uuid.NewString() },
		func(_ *Policy, p *Plan) { p.UserID = uuid.NewString() },
		func(_ *Policy, p *Plan) { p.ExpectedVersion = nil },
		func(_ *Policy, p *Plan) { p.ExpectedRoleID = nil },
		func(_ *Policy, p *Plan) { p.Action = "merge" },
		func(_ *Policy, p *Plan) { p.Reason = " " },
		func(c *Policy, _ *Plan) { c.NonemployeeOperators = nil },
		func(c *Policy, _ *Plan) { f := c.Profiles["triage"]; f.SubjectKind = ""; c.Profiles["triage"] = f },
		func(c *Policy, _ *Plan) {
			f := c.Profiles["triage"]
			f.Permissions = append(f.Permissions, "cases:read.own")
			c.Profiles["triage"] = f
		},
		func(c *Policy, _ *Plan) {
			f := c.Profiles["triage"]
			f.Permissions = append(f.Permissions, "security:mfa.revoke")
			c.Profiles["triage"] = f
		},
		func(c *Policy, _ *Plan) {
			f := c.Profiles["triage"]
			f.Permissions = append(f.Permissions, "security:breakglass.use")
			c.Profiles["triage"] = f
		},
		func(c *Policy, _ *Plan) {
			f := c.Profiles["triage"]
			f.Grants[0].UnitID = "*"
			c.Profiles["triage"] = f
		},
		func(c *Policy, _ *Plan) {
			f := c.Profiles["triage"]
			f.Grants[0].Permission = "cases:work.assigned"
			c.Profiles["triage"] = f
		},
		func(c *Policy, _ *Plan) { f := c.Profiles["triage"]; f.Kind = "routing"; c.Profiles["triage"] = f },
	} {
		policy, plan := fixturePlan()
		mutate(&policy, &plan)
		if _, err := validatePlan(policy, plan); err == nil {
			t.Fatal("invalid plan/profile accepted")
		}
	}
	policy, plan := fixturePlan()
	if _, err := validatePlan(policy, plan); err != nil {
		t.Fatal(err)
	}
	plan.Action, plan.Profile = "revoke", ""
	if _, err := validatePlan(policy, plan); err != nil {
		t.Fatal(err)
	}
}

func TestStrictJSONRejectsActorAndMultipleValues(t *testing.T) {
	for _, body := range []string{`{"operator_id":"forged"}`, `{} {}`, `{"permissions":["cases:read"]}`, `{"employee_id":"forged"}`, `{"employee_bindings":{"forged":{"phone":"+27820000017"}}}`} {
		var p Plan
		if err := decodeJSON(strings.NewReader(body), &p); err == nil {
			t.Fatal("untrusted actor/extra JSON accepted")
		}
	}
}
