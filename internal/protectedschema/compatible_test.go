package protectedschema

import (
	"github.com/shridarpatil/whatomate/internal/config"
	"strings"
	"testing"
)

func compatibleConfig(t *testing.T) (*config.Config, CompatibleRequest) {
	t.Helper()
	oldRelease, oldCatalog := ReleaseIdentity, ExpectedCatalogSHA256
	ReleaseIdentity = strings.Repeat("a", 40) + ":" + strings.Repeat("b", 40)
	ExpectedCatalogSHA256 = strings.Repeat("c", 64)
	t.Cleanup(func() { ReleaseIdentity = oldRelease; ExpectedCatalogSHA256 = oldCatalog })
	c := &config.Config{}
	c.App.Environment = "production"
	c.Database.Name = "compatible_test"
	c.Database.User = "compatible_owner"
	c.Database.SchemaManagedExternally = true
	c.Database.SchemaRelease = ReleaseIdentity
	c.Database.SchemaSHA256 = ExpectedCatalogSHA256
	c.Extensions.ProtectedMode = true
	c.Channels.StopDispatch = true
	c.DefaultAdmin.Email = "owner@example.invalid"
	return c, CompatibleRequest{PreviousRelease: strings.Repeat("d", 40) + ":" + strings.Repeat("b", 40), PreviousGeneration: 4, OrganizationID: "11111111-1111-4111-8111-111111111111"}
}

func TestCompatibleRequestRequiresDifferentCompiledReleaseAndHeldOwner(t *testing.T) {
	c, r := compatibleConfig(t)
	if err := ValidateCompatible(c, r); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*config.Config, *CompatibleRequest){
		"same release":           func(c *config.Config, r *CompatibleRequest) { r.PreviousRelease = ReleaseIdentity },
		"zero generation":        func(c *config.Config, r *CompatibleRequest) { r.PreviousGeneration = 0 },
		"bad previous":           func(c *config.Config, r *CompatibleRequest) { r.PreviousRelease = "captured" },
		"wrong catalog":          func(c *config.Config, r *CompatibleRequest) { c.Database.SchemaSHA256 = strings.Repeat("e", 64) },
		"wrong compiled release": func(c *config.Config, r *CompatibleRequest) { c.Database.SchemaRelease = r.PreviousRelease },
		"invalid org":            func(c *config.Config, r *CompatibleRequest) { r.OrganizationID = "arbitrary" },
		"unprotected":            func(c *config.Config, r *CompatibleRequest) { c.Extensions.ProtectedMode = false },
		"carrier runtime":        func(c *config.Config, r *CompatibleRequest) { c.Extensions.CarrierEnabled = true },
		"dispatch":               func(c *config.Config, r *CompatibleRequest) { c.Extensions.CarrierDispatchEnabled = true },
		"generic dispatch":       func(c *config.Config, r *CompatibleRequest) { c.Channels.StopDispatch = false },
		"plan":                   func(c *config.Config, r *CompatibleRequest) { c.Plan.Enabled = true },
		"missing owner":          func(c *config.Config, r *CompatibleRequest) { c.DefaultAdmin.Email = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := *c
			request := r
			change(&bad, &request)
			if ValidateCompatible(&bad, request) == nil {
				t.Fatal("unsafe request accepted")
			}
		})
	}
	if ValidateCompatible(nil, r) == nil {
		t.Fatal("nil config accepted")
	}
}
