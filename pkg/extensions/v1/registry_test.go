package extensions

import (
	"context"
	"errors"
	"testing"
)

func TestRegistryRejectsMissingRequiredModuleAndWrongAPI(t *testing.T) {
	r := NewRegistry()
	if err := r.Validate([]string{"cases"}); err == nil {
		t.Fatal("required module must fail startup when omitted")
	}
	if err := r.Register(Descriptor{ID: "cases", Version: "0.1.0", APIVersion: 2}); err == nil {
		t.Fatal("incompatible API accepted")
	}
	if err := r.Register(Descriptor{ID: "cases", Version: "0.1.0", APIVersion: APIVersion, Dependencies: []string{"directory"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate([]string{"cases"}); err == nil {
		t.Fatal("dependency missing")
	}
	if err := r.Register(Descriptor{ID: "directory", Version: "0.1.0", APIVersion: APIVersion}); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate([]string{"cases"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Descriptor{ID: "cases", Version: "0.2.0", APIVersion: APIVersion}); err == nil {
		t.Fatal("duplicate registration")
	}
}

func TestRegistryRejectsCyclesAndNamespaceEscapes(t *testing.T) {
	r := NewRegistry()
	for _, d := range []Descriptor{
		{ID: "a", Version: "1.0.0", APIVersion: 1, Dependencies: []string{"b"}},
		{ID: "b", Version: "1.0.0", APIVersion: 1, Dependencies: []string{"a"}},
	} {
		if err := r.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Validate(nil); err == nil {
		t.Fatal("cyclic dependency accepted")
	}
	for _, route := range []string{"/api/users", "/api/extensions/cases/../users", "/api/extensions/cases/%2e%2e/users"} {
		if err := (Descriptor{ID: "cases", Version: "1.0.0", APIVersion: 1, Routes: []Route{{Method: "GET", Path: route, Permission: "cases:read"}}}).Validate(); err == nil {
			t.Fatalf("unsafe route %q", route)
		}
	}
}

func TestProtectionDeniesDisabledModuleWrongOrgAndUnassignedActor(t *testing.T) {
	p, err := NewProtection("org-a", "cases", "case-1", "restricted")
	if err != nil {
		t.Fatal(err)
	}
	g := NewGuard()
	a := Actor{OrganizationID: "org-a", UserID: "user-a", Permissions: []string{"cases:read"}}
	if g.Authorize(context.Background(), a, p, "read") == nil {
		t.Fatal("missing policy must deny historical data")
	}
	g.Register("cases", func(_ context.Context, a Actor, p Protection, action string) error {
		if a.UserID != "assigned" || !a.Has("cases:read") {
			return ErrDenied
		}
		return nil
	})
	if g.Authorize(context.Background(), a, p, "read") == nil {
		t.Fatal("permission alone granted access")
	}
	a.UserID = "assigned"
	if err := g.Authorize(context.Background(), a, p, "read"); err != nil {
		t.Fatal(err)
	}
	a.OrganizationID = "org-b"
	if !errors.Is(g.Authorize(context.Background(), a, p, "read"), ErrDenied) {
		t.Fatal("cross-org access")
	}
}

func TestIngressRequiredPolicyFailsClosedAndDoesNotRetainPayload(t *testing.T) {
	g := NewGuard()
	in := Inbound{OrganizationID: "org-a", AccountID: "a", ProviderMessageID: "m", Sender: "+27123456789", Kind: "text"}
	if g.CheckInbound(context.Background(), in, []string{"directory"}) == nil {
		t.Fatal("absent eligibility policy admitted message")
	}
	g.RegisterInbound("directory", func(_ context.Context, in Inbound) error { return ErrDenied })
	if g.CheckInbound(context.Background(), in, []string{"directory"}) == nil {
		t.Fatal("revoked sender admitted")
	}
	if err := g.CheckInbound(context.Background(), in, nil); err != nil {
		t.Fatal("unmanaged host changed")
	}
}
