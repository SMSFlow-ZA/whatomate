package main

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/google/uuid"
)

// Policy is a deployment-owned file. The grant plan cannot supply an operator,
// permissions, scopes or target IDs outside these exact named profiles.
type Policy struct {
	OrganizationID       string                               `json:"organization_id"`
	OperatorID           string                               `json:"operator_id"`
	Profiles             map[string]Profile                   `json:"profiles"`
	NonemployeeOperators map[string]string                    `json:"nonemployee_operators,omitempty"`
	EmployeeBindings     map[string]directory.IdentityBinding `json:"employee_bindings,omitempty"`
}
type Profile struct {
	Kind        string      `json:"kind"`
	SubjectKind string      `json:"subject_kind,omitempty"`
	UserIDs     []string    `json:"user_ids"`
	Permissions []string    `json:"permissions"`
	Grants      []api.Grant `json:"grants"`
}
type Plan struct {
	OrganizationID  string  `json:"organization_id"`
	UserID          string  `json:"user_id"`
	Action          string  `json:"action"`
	Profile         string  `json:"profile"`
	ExpectedVersion *int64  `json:"expected_version"`
	ExpectedRoleID  *string `json:"expected_role_id"`
	Reason          string  `json:"reason"`
}

var profileName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var allowedPermissions = map[string]bool{
	"security:mfa.enroll": true,
	"directory:read":      true, "directory:import": true, "directory:approve": true, "directory:revoke": true,
	"knowledge:read": true, "knowledge:upload": true, "knowledge:job": true, "knowledge:review": true, "knowledge:publish": true, "knowledge:retire": true,
	"cases:read": true, "cases:submit.own": true, "cases:read.own": true, "cases:read.triage": true, "cases:assign.triage": true, "cases:work.assigned": true,
	"ai:assist": true, "ai:review": true,
	"reports:operational": true, "reports:aggregate": true, "reports:export": true,
}

func exactID(v string) bool {
	id, err := uuid.Parse(v)
	return err == nil && id != uuid.Nil && id.String() == v
}
func exactScope(v string) bool {
	return v != "" && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "*\x00\r\n") && len(v) <= 256
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func decodeJSON(r io.Reader, out any) error {
	data, err := io.ReadAll(io.LimitReader(r, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return errors.New("invalid or oversized JSON")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("one JSON value required")
	}
	return nil
}

func validatePolicy(policy Policy) error {
	if !exactID(policy.OrganizationID) || !exactID(policy.OperatorID) || len(policy.Profiles) == 0 || len(policy.Profiles) > 128 {
		return errors.New("exact organization, operator and profiles required")
	}
	for id, reference := range policy.NonemployeeOperators {
		if !exactID(id) || !exactScope(reference) {
			return errors.New("nonemployee operators require exact IDs and explicit reviewed attestation references")
		}
	}
	for id, binding := range policy.EmployeeBindings {
		if !exactID(id) || binding.Validate() != nil || policy.NonemployeeOperators[id] != "" {
			return errors.New("employee bindings require exact user and approved roster/phone identity; cannot also classify as nonemployee")
		}
	}
	for name, p := range policy.Profiles {
		if !profileName.MatchString(name) || len(p.UserIDs) == 0 || len(p.UserIDs) > 512 || len(p.Permissions) == 0 || len(p.Permissions) > 64 || len(p.Grants) == 0 || len(p.Grants) > 512 {
			return errors.New("invalid bounded profile")
		}
		if p.Kind != "operator" && p.Kind != "routing" && p.Kind != "escalation" && p.Kind != "directory-worker" && p.Kind != "knowledge-worker" {
			return errors.New("unknown profile kind")
		}
		if p.Kind == "operator" {
			if p.SubjectKind != "employee" && p.SubjectKind != "nonemployee" {
				return errors.New("human operator requires explicit employee/nonemployee classification")
			}
		} else if p.SubjectKind != "" {
			return errors.New("worker cannot assert human subject classification")
		}
		users := map[string]bool{}
		for _, id := range p.UserIDs {
			if !exactID(id) || users[id] {
				return errors.New("invalid or duplicate profile user")
			}
			users[id] = true
			if p.Kind == "operator" && p.SubjectKind == "nonemployee" && policy.NonemployeeOperators[id] == "" {
				return errors.New("nonemployee operator absent from separately attested policy allowlist")
			}
			if p.Kind == "operator" && p.SubjectKind == "employee" {
				if _, ok := policy.EmployeeBindings[id]; !ok {
					return errors.New("employee operator requires a deployment-owned exact employee/phone/roster-version binding")
				}
			}
		}
		permissions := map[string]bool{}
		for _, permission := range p.Permissions {
			if !allowedPermissions[permission] || permissions[permission] {
				return errors.New("permission outside explicit ER allowlist")
			}
			if p.SubjectKind == "nonemployee" && (permission == "cases:read.own" || permission == "cases:submit.own") {
				return errors.New("nonemployee operator cannot acquire employee self-service permissions")
			}
			if p.Kind != "operator" {
				allowed := false
				switch p.Kind {
				case "routing", "escalation":
					allowed = permission == "cases:read" || permission == "cases:read.triage" || permission == "cases:assign.triage" || permission == "cases:work.assigned"
				case "directory-worker":
					allowed = permission == "directory:read" || permission == "directory:import"
				case "knowledge-worker":
					allowed = permission == "knowledge:read" || permission == "knowledge:job"
				}
				if !allowed {
					return errors.New("worker profile cannot acquire human approval or authentication permissions")
				}
			}
			permissions[permission] = true
		}
		seen := map[api.Grant]bool{}
		for _, g := range p.Grants {
			if !permissions[g.Permission] || g.Permission == "security:mfa.enroll" || seen[g] || (g.UnitID == "") == (g.Audience == "") || g.UnitID != "" && !exactScope(g.UnitID) || g.Audience != "" && !exactScope(g.Audience) {
				return errors.New("profile requires unique exact permission scopes")
			}
			if strings.HasPrefix(g.Permission, "knowledge:") || strings.HasPrefix(g.Permission, "ai:") {
				if g.Audience == "" {
					return errors.New("audience scope required")
				}
			} else if g.UnitID == "" {
				return errors.New("unit scope required")
			}
			seen[g] = true
		}
	}
	return nil
}

func allowedTarget(policy Policy, user string) bool {
	for _, profile := range policy.Profiles {
		if contains(profile.UserIDs, user) {
			return true
		}
	}
	return false
}

func validatePlan(policy Policy, plan Plan) (Profile, error) {
	if err := validatePolicy(policy); err != nil {
		return Profile{}, err
	}
	if plan.OrganizationID != policy.OrganizationID || !exactID(plan.UserID) || !allowedTarget(policy, plan.UserID) || plan.ExpectedVersion == nil || *plan.ExpectedVersion < 0 || plan.ExpectedRoleID == nil || *plan.ExpectedRoleID != "" && !exactID(*plan.ExpectedRoleID) || !exactScope(plan.Reason) {
		return Profile{}, errors.New("exact permitted target, expected version/role and reason required")
	}
	if plan.Action == "revoke" && plan.Profile == "" {
		return Profile{}, nil
	}
	p, ok := policy.Profiles[plan.Profile]
	if plan.Action != "replace" || !ok || !contains(p.UserIDs, plan.UserID) {
		return Profile{}, errors.New("explicit replace/profile or revoke action required")
	}
	return p, nil
}
