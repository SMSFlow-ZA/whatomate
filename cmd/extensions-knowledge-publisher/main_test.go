package main

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictPlanCannotSupplyPrivilegesOrDuplicateFields(t *testing.T) {
	oldR, oldS := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	defer func() { protectedschema.ReleaseIdentity = oldR; protectedschema.ExpectedCatalogSHA256 = oldS }()
	protectedschema.ReleaseIdentity = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	protectedschema.ExpectedCatalogSHA256 = strings.Repeat("c", 64)
	p := Plan{Version: 1, InstanceID: uuid.NewString(), OrganizationID: uuid.NewString(), OperatorID: uuid.NewString(), UserID: uuid.NewString(), MembershipID: uuid.NewString(), Email: "synthetic-knowledge-publisher-1234abcd@example.invalid", Purpose: "automation-synthetic-knowledge-publisher", Generation: 4, Release: protectedschema.ReleaseIdentity, CatalogSHA256: protectedschema.ExpectedCatalogSHA256}
	raw, _ := json.Marshal(p)
	if _, e := decodePlan(raw); e != nil {
		t.Fatal("valid strict plan refused")
	}
	for _, body := range []string{string(raw[:len(raw)-1]) + `,"permissions":["*"]}`, string(raw[:len(raw)-1]) + `,"is_super_admin":true}`, string(raw[:len(raw)-1]) + `,"email":"synthetic-knowledge-publisher-1234abcd@example.invalid"}`, string(raw) + ` {}`} {
		if _, e := decodePlan([]byte(body)); e == nil {
			t.Fatal("untrusted plan accepted")
		}
	}
}
func TestPrivatePasswordAndAttemptBoundaries(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("Linux root filesystem boundary test")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated root test required")
	}
	base := filepath.Join("/root", "publisher-private-"+uuid.NewString())
	if e := os.Mkdir(base, 0700); e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(base)
	password := filepath.Join(base, "password")
	value := []byte("synthetic-fixture-private-password-never-output")
	if e := writePassword(password, value); e != nil {
		t.Fatal("private custody refused")
	}
	info, _ := os.Stat(password)
	if info.Mode().Perm() != 0600 {
		t.Fatal("password not private")
	}
	if e := writePassword(password, []byte("replacement")); e == nil {
		t.Fatal("password overwritten")
	}
	raw, e := readPrivate(password)
	if e != nil || string(raw) != string(value) {
		t.Fatal("existing password changed")
	}
	if e = os.Chmod(password, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = readPrivate(password); e == nil {
		t.Fatal("public password accepted")
	}
	os.Chmod(password, 0600)
	alias := filepath.Join(base, "alias")
	os.Symlink(password, alias)
	if _, e = readPrivate(alias); e == nil {
		t.Fatal("symlink read accepted")
	}
	if e = writePassword(alias, value); e == nil {
		t.Fatal("symlink replaced")
	}
	hard := filepath.Join(base, "hard")
	if e = os.Link(password, hard); e != nil {
		t.Fatal(e)
	}
	if _, e = readPrivate(password); e == nil {
		t.Fatal("hardlinked password accepted")
	}
	os.Remove(hard)
	attempt := filepath.Join(base, "attempt.json")
	if e = reserveAttempt(attempt, Result{AutomationSynthetic: true, PrimaryOnly: true}); e != nil {
		t.Fatal(e)
	}
	if e = reserveAttempt(attempt, Result{}); e == nil {
		t.Fatal("attempt replay accepted")
	}
	os.Chmod(base, 0777)
	if e = writePassword(filepath.Join(base, "unsafe"), value); e == nil {
		t.Fatal("writable parent accepted")
	}
	os.Chmod(base, 0700)
}
