package handlers

import (
	"context"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/notifications"
	"github.com/shridarpatil/whatomate/internal/config"
	"reflect"
	"testing"
)

func TestNotificationPrincipalUsesCurrentScopeWithoutInventingSession(t *testing.T) {
	member := addonapi.Actor{OrganizationID: "e79b89ba-533c-43a0-bdc7-b3319ba3b34b", UserID: "c5a00e47-da39-410f-8483-502214fb3c52", Permissions: []string{string(cases.WorkAssigned)}, Aliases: []string{"employee-current", "employee-old"}, Grants: []addonapi.Grant{{Permission: string(cases.WorkAssigned), UnitID: "unit-a"}, {Permission: string(cases.ReadTriage), UnitID: "unit-b"}}}
	p, err := extensionNotificationPrincipal(member)
	if err != nil {
		t.Fatalf("current scoped membership should support a minimal alert: %v", err)
	}
	if p.ID != member.UserID || !reflect.DeepEqual(p.Aliases, member.Aliases) || len(p.Grants) != 1 || p.Grants[0].UnitID != "unit-a" || p.Grants[0].Permission != cases.WorkAssigned {
		t.Fatalf("wrong background scope: %#v", p)
	}
	if member.SessionID != "" || member.MFA || member.PrimarySessionAuthorized {
		t.Fatal("background membership became an authenticated session")
	}
	p.Aliases[0] = "mutated"
	if member.Aliases[0] != "employee-current" {
		t.Fatal("principal aliases share mutable actor backing")
	}
	for _, mutate := range []func(*addonapi.Actor){
		func(a *addonapi.Actor) { a.Permissions = nil },
		func(a *addonapi.Actor) { a.Grants = nil },
		func(a *addonapi.Actor) { a.OrganizationID = "" },
		func(a *addonapi.Actor) { a.UserID = "" },
		func(a *addonapi.Actor) {
			a.Grants = []addonapi.Grant{{Permission: string(cases.WorkAssigned), UnitID: "*"}}
		},
	} {
		candidate := member
		mutate(&candidate)
		if _, err := extensionNotificationPrincipal(candidate); err == nil {
			t.Fatal("incomplete current scope accepted")
		}
	}
}

func TestNotificationModuleCannotStartWithoutBoundCarrier(t *testing.T) {
	a := App{Config: &config.Config{Extensions: config.ExtensionsConfig{Modules: "notifications"}}}
	if err := a.initializeProtectedCarrier(context.Background(), nil); err == nil {
		t.Fatal("notification module silently started without a bound carrier")
	}
}

func TestNotificationDisableClearsPriorService(t *testing.T) {
	a := App{Config: &config.Config{}, ExtensionNotifications: map[string]*notifications.Service{"old": {}}}
	if err := a.initializeProtectedCarrier(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if a.ExtensionNotifications != nil {
		t.Fatal("disabled module retained its background projector")
	}
}

func TestNotificationDeliveryPurposeCannotBorrowEmployeeAuthority(t *testing.T) {
	calls := 0
	a := App{ExtensionCarrier: &carrier.Service{AuthorizeHandler: func(context.Context, delivery.Message) error { calls++; return nil }}}
	for _, m := range []delivery.Message{
		{Purpose: "unknown", Channel: "whatsapp"},
		{RecipientUserID: "forged-handler", Channel: "whatsapp"},
		{EventVersion: 1, Channel: "whatsapp"},
		{Purpose: delivery.HandlerEscalation, Channel: "sms"},
	} {
		if err := a.authorizeProtectedDelivery(context.Background(), m); err == nil {
			t.Fatal("invalid purpose accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid purpose reached handler authorizer")
	}
	if err := a.authorizeProtectedDelivery(context.Background(), delivery.Message{Purpose: delivery.HandlerEscalation, Channel: "whatsapp"}); err != nil || calls != 1 {
		t.Fatal("handler-specific authorization not used", err)
	}
}
