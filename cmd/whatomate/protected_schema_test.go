package main

import (
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"strings"
	"testing"
)

func TestProtectedSchemaPreflightRequiresMatchingCompiledSource(t *testing.T) {
	oldR, oldS := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity = oldR; protectedschema.ExpectedCatalogSHA256 = oldS })
	protectedschema.ReleaseIdentity = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	protectedschema.ExpectedCatalogSHA256 = strings.Repeat("c", 64)
	c := &config.Config{}
	c.Extensions.ProtectedMode = true
	c.Database.SchemaManagedExternally = true
	c.Database.SchemaRelease = protectedschema.ReleaseIdentity
	c.Database.SchemaSHA256 = protectedschema.ExpectedCatalogSHA256
	if preflightExternalSchema(c, false, strings.Repeat("a", 40)) != nil {
		t.Fatal("matching artifact refused")
	}
	for _, source := range []string{"", strings.Repeat("d", 40)} {
		if preflightExternalSchema(c, false, source) == nil {
			t.Fatal("mismatched artifact accepted")
		}
	}
	if preflightExternalSchema(c, true, strings.Repeat("a", 40)) == nil {
		t.Fatal("runtime -migrate accepted")
	}
	c.Database.SchemaManagedExternally = false
	if preflightExternalSchema(c, true, "") != nil {
		t.Fatal("ordinary startup changed")
	}
	if preflightStandaloneWorker(c) == nil {
		t.Fatal("protected standalone worker accepted")
	}
	c.Extensions.ProtectedMode = false
	if preflightStandaloneWorker(c) != nil {
		t.Fatal("ordinary standalone worker changed")
	}
	c.Database.SchemaManagedExternally = true
	if preflightStandaloneWorker(c) == nil {
		t.Fatal("external standalone worker accepted")
	}
}
