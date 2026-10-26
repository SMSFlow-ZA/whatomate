//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealOwnerTLSCLIPreviewCreateReconcileAndCertificateRefusals(t *testing.T) {
	if os.Getenv("DEMO_IDENTITIES_TLS_TEST_DSN") == "" {
		t.Skip("dedicated disposable TLS fixture explicitly required")
	}
	if os.Getenv("DEMO_IDENTITIES_TLS_PUBLIC_EMPTY") != "true" {
		t.Fatal("dedicated empty public-schema TLS fixture acknowledgement required")
	}
	t.Setenv("DEMO_IDENTITIES_TEST_DSN", os.Getenv("DEMO_IDENTITIES_TLS_TEST_DSN"))
	db, c, p, _ := fixtureAt(t, true)
	dir, e := os.MkdirTemp("/root", "demo-owner-tls-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	put := func(name string, raw []byte) string {
		path := filepath.Join(dir, name)
		if e := os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	ca, e := os.ReadFile(os.Getenv("DEMO_IDENTITIES_TLS_CA_FILE"))
	if e != nil {
		t.Fatal(e)
	}
	wrongCA, e := os.ReadFile(os.Getenv("DEMO_IDENTITIES_TLS_WRONG_CA_FILE"))
	if e != nil {
		t.Fatal(e)
	}
	secret, e := os.ReadFile(os.Getenv("DEMO_IDENTITIES_TLS_PASSWORD_FILE"))
	if e != nil {
		t.Fatal(e)
	}
	c.Database.Host = "localhost"
	c.Database.Port = 5432
	c.Database.Password = strings.TrimSpace(string(secret))
	c.Database.SSLMode = "verify-full"
	configBytes := func(host string) []byte {
		return []byte(fmt.Sprintf("[app]\nenvironment=\"production\"\ndebug=false\n[database]\nhost=%q\nport=5432\nname=%q\nuser=%q\npassword=%q\nssl_mode=\"verify-full\"\nschema_managed_externally=true\nschema_release=%q\nschema_sha256=%q\n[extensions]\nprotected_mode=true\n", host, c.Database.Name, c.Database.User, c.Database.Password, p.Release, p.CatalogSHA256))
	}
	cfg := put("owner.toml", configBytes("localhost"))
	badHost := put("wrong-host.toml", configBytes("127.0.0.1"))
	caFile := put("ca.pem", ca)
	badCA := put("wrong-ca.pem", wrongCA)
	planRaw, _ := json.Marshal(p)
	planFile := put("plan.json", planRaw)
	attempt := filepath.Join(dir, "attempt.json")
	passwordsFile := filepath.Join(dir, "passwords.json")
	args := func(config, ca string) []string {
		return []string{"-config", config, "-ca-file", ca, "-plan", planFile, "-attempt", attempt, "-password-out", passwordsFile}
	}
	for _, negative := range []struct{ name, config, ca string }{{"bad-ca", cfg, badCA}, {"hostname", badHost, caFile}} {
		t.Run(negative.name, func(t *testing.T) {
			var out bytes.Buffer
			if run(args(negative.config, negative.ca), &out) == nil {
				t.Fatal("untrusted TLS peer accepted")
			}
			if strings.Contains(out.String(), c.Database.Password) {
				t.Fatal("credential disclosure")
			}
			var n int
			if e = db.QueryRow("SELECT count(*) FROM users").Scan(&n); e != nil || n != 1 {
				t.Fatal("TLS refusal changed DB", n, e)
			}
		})
	}
	var out bytes.Buffer
	if e = run(args(cfg, caFile), &out); e != nil {
		t.Fatal("actual TLS preview refused", e)
	}
	var result Result
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Outcome != "preview-validated" || result.Applied {
		t.Fatal("incorrect preview")
	}
	if _, e = os.Stat(attempt); !os.IsNotExist(e) {
		t.Fatal("preview reserved attempt")
	}
	out.Reset()
	if e = run(append(args(cfg, caFile), "-apply"), &out); e != nil {
		t.Fatal("actual TLS create refused", e)
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || !result.Applied || result.Outcome != "committed-observed" {
		t.Fatal("incorrect observed apply")
	}
	out.Reset()
	if e = run(append(args(cfg, caFile), "-reconcile"), &out); e != nil {
		t.Fatal("actual TLS readonly reconciliation refused", e)
	}
	result = Result{}
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Applied || result.Outcome != "committed-observed" {
		t.Fatal("incorrect readonly reconciliation")
	}
	secretRaw, e := readPrivate(passwordsFile)
	if e != nil {
		t.Fatal(e)
	}
	var passwords map[string]string
	if json.Unmarshal(secretRaw, &passwords) != nil || len(passwords) != 6 {
		t.Fatal("private password custody incomplete")
	}
	for _, password := range passwords {
		if len(password) < 32 || strings.Contains(out.String(), password) {
			t.Fatal("private password leaked")
		}
	}
	out.Reset()
	if e = run(append(args(cfg, caFile), "-apply"), &out); e == nil {
		t.Fatal("immutable attempt replay accepted")
	}
}
