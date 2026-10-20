package extensions

import (
	"context"
	"testing"
)

func TestManifestCannotMutateRegisteredPermissions(t *testing.T) {
	r := NewRegistry()
	d := Descriptor{ID: "cases", Version: "1.0.0", APIVersion: 1, Routes: []Route{{Method: "GET", Path: "/api/extensions/cases/items", Permission: "cases:read"}}}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	d.Routes[0].Permission = "cases:all"
	m := r.Manifest()
	m[0].Routes[0].Permission = "cases:all"
	if r.Manifest()[0].Routes[0].Permission != "cases:read" {
		t.Fatal("manifest mutated registration")
	}
}
func TestGuardRejectsDuplicatePolicy(t *testing.T) {
	g := NewGuard()
	p := func(context.Context, Actor, Protection, string) error { return nil }
	if err := g.Register("cases", p); err != nil {
		t.Fatal(err)
	}
	if g.Register("cases", p) == nil {
		t.Fatal("policy silently replaced")
	}
}
