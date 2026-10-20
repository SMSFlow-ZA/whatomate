package extensions

import (
	"context"
	"errors"
	"testing"
)

func TestGuardContainsPolicyPanicsAndDoesNotExposePolicyErrors(t *testing.T) {
	p, _ := NewProtection("org", "cases", "case", "restricted")
	a := Actor{OrganizationID: "org", UserID: "handler"}
	for _, policy := range []Policy{func(context.Context, Actor, Protection, string) error { panic("sensitive payload") }, func(context.Context, Actor, Protection, string) error { return errors.New("sensitive payload") }} {
		g := NewGuard()
		if err := g.Register("cases", policy); err != nil {
			t.Fatal(err)
		}
		if err := g.Authorize(context.Background(), a, p, "read"); err != ErrDenied {
			t.Fatalf("policy did not fail closed with generic error: %v", err)
		}
	}
	g := NewGuard()
	g.RegisterInbound("directory", func(context.Context, Inbound) error { panic("sensitive sender") })
	if err := g.CheckInbound(context.Background(), Inbound{OrganizationID: "org", ProviderMessageID: "m", Sender: "+27123456789"}, []string{"directory"}); err != ErrDenied {
		t.Fatal("inbound policy panic admitted message")
	}
}
func TestDescriptorRejectsPermissionWildcardsAndURLQuery(t *testing.T) {
	for _, route := range []Route{{Method: "GET", Path: "/api/extensions/cases/list", Permission: "cases:*"}, {Method: "GET", Path: "/api/extensions/cases/list?other=1", Permission: "cases:read"}, {Method: "GET", Path: "/api/extensions/cases/list", Permission: "cases:read:all"}} {
		if err := (Descriptor{ID: "cases", Version: "1.0.0", APIVersion: 1, Routes: []Route{route}}).Validate(); err == nil {
			t.Fatalf("unsafe descriptor accepted %+v", route)
		}
	}
}
