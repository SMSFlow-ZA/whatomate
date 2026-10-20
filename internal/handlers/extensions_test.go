package handlers

import (
	"context"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"testing"
)

func TestExtensionEligibilityGateFailsClosedWithoutPolicy(t *testing.T) {
	a := &App{Config: &config.Config{Extensions: config.ExtensionsConfig{RequiredInbound: "directory"}}}
	if err := a.checkExtensionInbound(context.Background(), uuid.New(), "account", IncomingTextMessage{ID: "msg", From: "+27123456789", Type: "text"}); err == nil {
		t.Fatal("missing policy admitted sender")
	}
	a.ExtensionGuard = ext.NewGuard()
	if err := a.ExtensionGuard.RegisterInbound("directory", func(context.Context, ext.Inbound) error { return ext.ErrDenied }); err != nil {
		t.Fatal(err)
	}
	if err := a.checkExtensionInbound(context.Background(), uuid.New(), "account", IncomingTextMessage{ID: "msg", From: "+27123456789", Type: "text"}); err == nil {
		t.Fatal("ineligible sender admitted")
	}
}
func TestExtensionEligibilityGateLeavesUnconfiguredHostUnchanged(t *testing.T) {
	a := &App{Config: &config.Config{}}
	if err := a.checkExtensionInbound(context.Background(), uuid.New(), "a", IncomingTextMessage{}); err != nil {
		t.Fatal(err)
	}
}
func TestInitializeExtensionsRejectsUncompiledModulesAndProtectedMode(t *testing.T) {
	for _, c := range []config.ExtensionsConfig{{Required: "cases"}, {RequiredInbound: "directory"}, {ProtectedMode: true}} {
		a := &App{Config: &config.Config{Extensions: c}}
		if a.InitializeExtensions() == nil {
			t.Fatalf("unsupported extension deployment started: %+v", c)
		}
	}
	if err := (&App{Config: &config.Config{}}).InitializeExtensions(); err != nil {
		t.Fatal(err)
	}
}
