//go:build linux

package main

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfiguration() configuration {
	now := time.Now().UTC()
	hex := "11111111222243338444555555555555"
	root := "/srv/whatomate/er/" + hex
	return configuration{InstanceID: "11111111-2222-4333-8444-555555555555", OrganizationID: "22222222-3333-4444-8555-666666666666", WorkerUserID: "33333333-4444-4555-8666-777777777777", ProfileID: "reviewed-worker-v1", InstanceRoot: root, DatabaseName: "er_" + hex, DatabaseRole: "er_processing_" + hex, DatabaseDSNFile: root + "/config/processing.dsn", Audiences: []string{"staff"}, DefinitionsVerifiedAt: now.Add(-time.Hour), DefinitionsValidUntil: now.Add(time.Hour), AuthorizationFunctionSHA256: strings.Repeat("c", 64), DefinitionsReviewSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Supervisor: documents.SupervisorConfig{JobRoot: root + "/jobs", DefinitionsRoot: root + "/definitions", MaxConcurrent: 1, CPUs: 1, DockerPath: "/usr/bin/docker", Image: "registry.example.invalid/worker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Worker: documents.Config{SignaturesValidUntil: now.Add(time.Hour)}}}
}
func TestProductionConfigurationRequiresExactPrivateBindings(t *testing.T) {
	c := validConfiguration()
	if err := c.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*configuration){func(c *configuration) { c.OrganizationID = "" }, func(c *configuration) { c.DatabaseRole = "postgres" }, func(c *configuration) { c.Supervisor.MaxConcurrent = 2 }, func(c *configuration) { c.Supervisor.JobRoot = "/tmp/jobs" }, func(c *configuration) { c.Audiences = []string{"*"} }, func(c *configuration) { c.DefinitionsValidUntil = time.Now().Add(-time.Second) }, func(c *configuration) { c.DefinitionsVerifiedAt = time.Now().Add(time.Hour) }, func(c *configuration) { c.DatabaseDSNFile = "/tmp/shared.dsn" }} {
		c := validConfiguration()
		mutate(&c)
		if err := c.validate(time.Now()); err == nil {
			t.Fatal("unsafe production configuration accepted")
		}
	}
}

func TestProductionMilliCPUQuotaIsValidatedBeforeDefinitionContainers(t *testing.T) {
	c := validConfiguration()
	c.Supervisor.CPUs = 0
	c.Supervisor.CPUMillis = 500
	if e := c.validate(time.Now()); e != nil {
		t.Fatal("500m configuration refused", e)
	}
	for _, bad := range []int{-1, 99, 4001} {
		c.Supervisor.CPUMillis = bad
		if c.validate(time.Now()) == nil {
			t.Fatal("invalid CPU bound accepted", bad)
		}
	}
	c.Supervisor.CPUs = 1
	c.Supervisor.CPUMillis = 500
	if c.validate(time.Now()) == nil {
		t.Fatal("ambiguous CPU settings accepted")
	}
}

func TestInvalidSupervisorNeverExecutesDefinitionsContainers(t *testing.T) {
	c := validConfiguration()
	jobs := t.TempDir()
	defs := t.TempDir()
	if err := os.Chmod(jobs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defs, "main.cvd"), []byte("synthetic-constructor-only"), 0400); err != nil {
		t.Fatal(err)
	}
	digest, err := documents.DefinitionsDigest(defs)
	if err != nil {
		t.Fatal(err)
	}
	c.Supervisor = documents.SupervisorConfig{JobRoot: jobs, DefinitionsRoot: defs, DefinitionsHash: digest, DockerPath: "/bin/true", Image: "registry.example.invalid/worker@sha256:" + strings.Repeat("a", 64), MaxConcurrent: 1, MemoryMiB: 128, CPUs: 1, PIDs: 16, TmpfsMiB: 32, Timeout: time.Minute, Worker: documents.Config{QuarantineRoot: "/tmp/quarantine", DatabasePath: "/var/lib/clamav", ScannerRevision: "constructor-only", ExtractionRevision: "constructor-only", OCRRevision: "constructor-only", Tools: documents.Tools{ClamScan: "/bin/true", PDFInfo: "/bin/true", PDFToText: "/bin/true", PDFToPPM: "/bin/true", Tesseract: "/bin/true"}, SignaturesValidUntil: time.Now().Add(time.Hour), MaxInputBytes: 1024, MaxPages: 1, MaxPageTextBytes: 64, MaxTotalTextBytes: 64, MaxRasterBytes: 1024, CommandTimeout: time.Second, JobTimeout: 2 * time.Second}}
	calls := 0
	verify := func(context.Context, configuration) error { calls++; return nil }
	if _, err := prepareSupervisor(context.Background(), c, verify); err != nil || calls != 1 {
		t.Fatalf("valid constructor control: calls=%d %v", calls, err)
	}
	c.Supervisor.Image = "unreviewed:latest"
	calls = 0
	if _, err := prepareSupervisor(context.Background(), c, verify); err == nil {
		t.Fatal("mutable image accepted")
	}
	if calls != 0 {
		t.Fatal("containers executed before supervisor image validation")
	}
}

func TestOwnerMigrationBootstrapNeverCreatesFakeRuntimeHash(t *testing.T) {
	c := validConfiguration()
	c.AuthorizationFunctionSHA256 = ""
	if c.validate(time.Now()) == nil {
		t.Fatal("runtime started without reviewed function hash")
	}
	migration, err := authorizationMigration(c)
	if err != nil || !strings.Contains(migration, "REVOKE ALL ON FUNCTION "+authorizationSignature+" FROM PUBLIC") {
		t.Fatalf("owner migration bootstrap %v", err)
	}
	c.Audiences = []string{"unsafe$er_owner$delimiter"}
	if _, err := authorizationMigration(c); err == nil {
		t.Fatal("SQL body delimiter accepted in policy literal")
	}
}
