package handlers

import (
	"context"
	"testing"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
)

func TestExtensionRefreshRejectsCallerActorWithoutSignedContext(t *testing.T) {
	a := &App{}
	forged := addonapi.Actor{OrganizationID: "o", UserID: "u", SessionID: "s", MFA: true, Permissions: []string{"ai:assist"}}
	if _, e := a.refreshExtensionActor(context.Background(), forged); e == nil {
		t.Fatal("caller actor became a current session")
	}
	if a.authorizeExtensionKnowledge(context.Background(), "invalid", "invalid", "knowledge:read", "employees") {
		t.Fatal("invalid membership authorized")
	}
}

func TestExtensionAccessSnapshotIgnoresOrderButDetectsRevocationAndAliases(t *testing.T) {
	before := addonapi.Actor{Permissions: []string{"cases:read.own", "knowledge:read"}, Aliases: []string{"b", "a"}, Grants: []addonapi.Grant{{Permission: "cases:read.own", UnitID: "u"}, {Permission: "knowledge:read", Audience: "employees"}}}
	after := before
	after.Permissions = []string{"knowledge:read", "cases:read.own"}
	after.Aliases = []string{"a", "b"}
	after.Grants = []addonapi.Grant{before.Grants[1], before.Grants[0]}
	if !sameExtensionAccess(before, after) {
		t.Fatal("order changed authorization")
	}
	after.Grants = after.Grants[:1]
	if sameExtensionAccess(before, after) {
		t.Fatal("removed scope accepted")
	}
	after = before
	after.Aliases = []string{"a", "b", "subject"}
	if sameExtensionAccess(before, after) {
		t.Fatal("new subject identity accepted")
	}
}
