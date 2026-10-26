package main

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"io"
	"regexp"
)

var fixtureLabels = []string{"employee", "manager", "hrbp", "er", "leadership", "unauthorized"}

type Identity struct {
	Fixture      string `json:"fixture"`
	UserID       string `json:"user_id"`
	MembershipID string `json:"membership_id"`
	Email        string `json:"email"`
}
type Plan struct {
	Version        int        `json:"version"`
	InstanceID     string     `json:"instance_id"`
	OrganizationID string     `json:"organization_id"`
	OperatorID     string     `json:"operator_id"`
	Purpose        string     `json:"purpose"`
	AttemptNonce   string     `json:"attempt_nonce"`
	Generation     int64      `json:"generation"`
	Release        string     `json:"release"`
	CatalogSHA256  string     `json:"catalog_sha256"`
	Identities     []Identity `json:"identities"`
}
type IdentityResult struct {
	Fixture      string `json:"fixture"`
	UserID       string `json:"user_id"`
	MembershipID string `json:"membership_id"`
}
type Result struct {
	OrganizationID       string           `json:"organization_id"`
	PlanSHA256           string           `json:"plan_sha256"`
	Identities           []IdentityResult `json:"identities"`
	Applied              bool             `json:"applied"`
	Outcome              string           `json:"outcome"`
	SyntheticFixtures    bool             `json:"synthetic_fixtures"`
	PrimaryOnly          bool             `json:"primary_identity_only"`
	HumanApprovalClaimed bool             `json:"human_approval_claimed"`
	AutomaticRetry       bool             `json:"automatic_retry"`
}

func validPlan(p Plan) error {
	seen := map[string]bool{}
	check := func(id string) bool {
		v, e := uuid.Parse(id)
		if e != nil || v == uuid.Nil || v.String() != id || seen[id] {
			return false
		}
		seen[id] = true
		return true
	}
	if p.Version != 1 || p.Purpose != "synthetic-role-acceptance" || p.Generation < 1 || p.Release != protectedschema.ReleaseIdentity || p.CatalogSHA256 != protectedschema.ExpectedCatalogSHA256 || !regexp.MustCompile(`^[a-f0-9]{40}:[a-f0-9]{40}$`).MatchString(p.Release) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(p.CatalogSHA256) || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(p.AttemptNonce) || !check(p.InstanceID) || !check(p.OrganizationID) || !check(p.OperatorID) || len(p.Identities) != len(fixtureLabels) {
		return refused
	}
	for i, v := range p.Identities {
		if v.Fixture != fixtureLabels[i] || !check(v.UserID) || !check(v.MembershipID) || !regexp.MustCompile(`^synthetic-role-`+fixtureLabels[i]+`-[a-f0-9]{8}@example\.invalid$`).MatchString(v.Email) {
			return refused
		}
	}
	return nil
}

// Reject duplicate keys at every nesting level before strict typed decoding.
func jsonValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return refused
	}
	tok, e := d.Token()
	if e != nil {
		return refused
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			s, ok := key.(string)
			if e != nil || !ok || seen[s] {
				return refused
			}
			seen[s] = true
			if jsonValue(d, depth+1) != nil {
				return refused
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return refused
		}
	case '[':
		for d.More() {
			if jsonValue(d, depth+1) != nil {
				return refused
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return refused
		}
	default:
		return refused
	}
	return nil
}
func decodePlan(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > 1<<20 {
		return p, refused
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	if jsonValue(scan, 0) != nil {
		return p, refused
	}
	if _, e := scan.Token(); e != io.EOF {
		return p, refused
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return p, refused
	}
	return p, validPlan(p)
}
