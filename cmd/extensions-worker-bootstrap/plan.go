// extensions-worker-bootstrap is an offline owner tool, never an HTTP endpoint.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var denied = errors.New("worker_bootstrap_refused")

type Plan struct {
	rawSHA256         string
	Version           int         `json:"version"`
	Nonce             string      `json:"attempt_nonce"`
	InstanceID        string      `json:"instance_id"`
	OrganizationID    string      `json:"organization_id"`
	OperatorID        string      `json:"operator_id"`
	WorkerID          string      `json:"worker_id"`
	RoleID            string      `json:"custom_role_id"`
	MembershipID      string      `json:"membership_id"`
	PermissionID      string      `json:"permission_id"`
	AuthStamp         string      `json:"auth_stamp"`
	Email             string      `json:"email"`
	DisplayName       string      `json:"display_name"`
	Profile           string      `json:"profile"`
	Audiences         []string    `json:"audiences"`
	ApprovalReference string      `json:"approval_reference"`
	Reason            string      `json:"reason"`
	ExpectedVersion   int64       `json:"expected_access_version"`
	Generation        int64       `json:"generation"`
	Release           string      `json:"release"`
	CatalogSHA256     string      `json:"catalog_sha256"`
	FunctionSHA256    string      `json:"function_sha256"`
	BeforeACL         Observation `json:"before_acl"`
}

func planHash(p Plan) string {
	if p.rawSHA256 != "" {
		return p.rawSHA256
	}
	b, _ := json.Marshal(p)
	return digest(b)
}

func exactID(s string) bool {
	v, e := uuid.Parse(s)
	return e == nil && v != uuid.Nil && v.String() == s
}
func exactText(s string, n int) bool {
	return s != "" && len(s) <= n && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "*\x00\r\n")
}
func sha(s string) bool      { return regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(s) }
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (p Plan) names() (string, string, string, string) {
	h := strings.ReplaceAll(p.InstanceID, "-", "")
	return "er_" + h, "er_owner_" + h, "er_processing_" + h, "er_processing_owner_" + h
}
func (p Plan) Validate() error {
	if p.Version != 1 || p.ExpectedVersion != 0 || p.Generation < 1 || !regexp.MustCompile(`^[a-f0-9]{40}:[a-f0-9]{40}$`).MatchString(p.Release) || !sha(p.CatalogSHA256) || !sha(p.FunctionSHA256) {
		return denied
	}
	seen := map[string]bool{}
	for _, v := range []string{p.Nonce, p.InstanceID, p.OrganizationID, p.OperatorID, p.WorkerID, p.RoleID, p.MembershipID, p.PermissionID, p.AuthStamp} {
		if !exactID(v) || seen[v] {
			return denied
		}
		seen[v] = true
	}
	if p.Email != "worker-"+p.WorkerID+"@example.invalid" || !exactText(p.DisplayName, 100) || !regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`).MatchString(p.Profile) || !exactText(p.ApprovalReference, 256) || !exactText(p.Reason, 512) || len(p.Audiences) == 0 || len(p.Audiences) > 32 {
		return denied
	}
	seen = map[string]bool{}
	for _, v := range p.Audiences {
		if !exactText(v, 200) || seen[v] {
			return denied
		}
		seen[v] = true
	}
	if p.BeforeACL.Version == 0 && len(p.BeforeACL.Records) == 0 {
		return nil
	} // Initial inspect only; executable modes require the full observation.
	return p.BeforeACL.Validate()
}
func decode(b []byte, out any) error {
	if len(b) > 16*1024*1024 {
		return denied
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return denied
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return denied
	}
	return nil
}
