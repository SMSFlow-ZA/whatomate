package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTypedRefusalStageDoesNotExposeUnderlyingSecret(t *testing.T) {
	p := fixturePlan()
	s := Session{}
	r := s.failedResult(p, refuseStage("catalog_capture_refused"))
	if r.Code != "catalog_capture_refused" || r.Outcome != "refused" {
		t.Fatalf("missing bounded diagnostic: %s %s", r.Outcome, r.Code)
	}
	if strings.Contains(refuseStage("catalog_capture_refused").Error(), "catalog_capture_refused") {
		t.Fatal("error transport should remain generic")
	}
	r = s.failedResult(p, errCommitUnknown)
	if r.Outcome != "unknown-held" || r.Code != "commit_unknown" {
		t.Fatal("unknown commit semantics changed")
	}
}

func TestDriverErrorNeverSerialized(t *testing.T) {
	r := (&Session{}).failedResult(fixturePlan(), errors.New("secret-password SELECT confidential driver text"))
	raw, e := json.Marshal(r)
	if e != nil || strings.Contains(string(raw), "secret-password") || strings.Contains(string(raw), "confidential") || strings.Contains(string(raw), "SELECT") {
		t.Fatal("unsafe driver diagnostic")
	}
	if r.Code != "precondition_or_transaction_refused" {
		t.Fatal("unexpected raw diagnostic")
	}
}
