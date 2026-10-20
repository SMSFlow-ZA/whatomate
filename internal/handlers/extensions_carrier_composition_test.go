package handlers

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/shridarpatil/whatomate/internal/config"
	"testing"
)

func TestCarrierCompositionDisabledDefaultAndInvalidDependencies(t *testing.T) {
	disabled := App{Config: &config.Config{}}
	if e := disabled.initializeProtectedCarrier(context.Background(), nil); e != nil || disabled.ExtensionCarrier != nil {
		t.Fatal("default carrier must be absent")
	}
	for _, cfg := range []config.ExtensionsConfig{{CarrierEnabled: true}, {CarrierDispatchEnabled: true}, {CarrierBindings: []config.ExtensionCarrierBinding{{OrganizationID: "untrusted"}}}} {
		a := App{Config: &config.Config{Extensions: cfg}}
		if e := a.initializeProtectedCarrier(context.Background(), nil); e == nil {
			t.Fatal("incomplete or contradictory carrier composition accepted")
		}
	}
}
func TestCarrierCredentialsRequireCurrentComposition(t *testing.T) {
	a := App{Config: &config.Config{}}
	if _, e := a.protectedCarrierCredentials(context.Background(), delivery.Message{}); e == nil {
		t.Fatal("unbound carrier can resolve credentials")
	}
}
