package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/test/testutil"
)

func TestERConversationConfigurationSeparatesProductionAndRehearsal(t *testing.T) {
	base := config.ExtensionsConfig{ProtectedMode: true, RosterFreshnessSeconds: 86400, EscalationAfterSeconds: 3600, ConversationEmployeeAudience: "employees"}
	if err := validateExtensionConversationConfig(base); err != nil {
		t.Fatalf("explicit protected production settings rejected: %v", err)
	}
	for name, mutate := range map[string]func(*config.ExtensionsConfig){
		"unprotected production": func(c *config.ExtensionsConfig) { c.ProtectedMode = false },
		"zero freshness":         func(c *config.ExtensionsConfig) { c.RosterFreshnessSeconds = 0 },
		"unbounded freshness":    func(c *config.ExtensionsConfig) { c.RosterFreshnessSeconds = 1 << 62 },
		"zero escalation":        func(c *config.ExtensionsConfig) { c.EscalationAfterSeconds = 0 },
		"unbounded escalation":   func(c *config.ExtensionsConfig) { c.EscalationAfterSeconds = 1 << 62 },
		"wildcard audience":      func(c *config.ExtensionsConfig) { c.ConversationEmployeeAudience = "employees/*" },
		"blank audience":         func(c *config.ExtensionsConfig) { c.ConversationEmployeeAudience = " " },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if validateExtensionConversationConfig(c) == nil {
				t.Fatal("unsafe settings accepted")
			}
		})
	}
	legacy := base
	legacy.ProtectedMode = false
	legacy.SyntheticDevelopment = true
	if err := validateExtensionConversationConfig(legacy); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedReleaseUnboundWebhookDoesNotFallThrough(t *testing.T) {
	a := &App{Config: &config.Config{Extensions: config.ExtensionsConfig{ProtectedMode: true}}}
	r := testutil.NewRequest(t)
	handled, err := a.protectedCarrierWebhook(r, WebhookPayload{}, []byte(`{}`), nil)
	if err != nil || !handled || r.RequestCtx.Response.StatusCode() != 403 {
		t.Fatal("unbound protected webhook fell through to generic host")
	}
}

func TestProtectedReleaseRecipientRestrictionAppliesOutsideDevelopment(t *testing.T) {
	c := config.ExtensionsConfig{CarrierAllowedRecipients: []string{"+27820000001"}}
	if !extensionCarrierRecipientAllowed(c, "+27820000001") || extensionCarrierRecipientAllowed(c, "+27820000002") {
		t.Fatal("production test-recipient restriction ignored")
	}
	c.CarrierAllowedRecipients = nil
	if !extensionCarrierRecipientAllowed(c, "+27820000002") {
		t.Fatal("ordinary production eligibility policy unexpectedly restricted")
	}
	c.SyntheticDevelopment = true
	if extensionCarrierRecipientAllowed(c, "+27820000002") {
		t.Fatal("development without explicit recipients admitted send")
	}
}

func TestProtectedReleaseRequiresCompleteExplicitComposition(t *testing.T) {
	base := config.ExtensionsConfig{ProtectedMode: true, Modules: "directory,cases,knowledge,ai,conversation,reports,governance", AIProvider: "https", MFAFreshnessSeconds: 900, ReportsMinGroup: 5, RosterFreshnessSeconds: 86400, EscalationAfterSeconds: 3600, ConversationEmployeeAudience: "employees"}
	if err := validateProtectedExtensionSettings(base); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*config.ExtensionsConfig){
		"development composition":     func(c *config.ExtensionsConfig) { c.SyntheticDevelopment = true },
		"missing module":              func(c *config.ExtensionsConfig) { c.Modules = "directory,cases" },
		"implicit provider":           func(c *config.ExtensionsConfig) { c.AIProvider = "" },
		"expired MFA policy":          func(c *config.ExtensionsConfig) { c.MFAFreshnessSeconds = 0 },
		"unbounded MFA policy":        func(c *config.ExtensionsConfig) { c.MFAFreshnessSeconds = 86400 },
		"unsafe disclosure threshold": func(c *config.ExtensionsConfig) { c.ReportsMinGroup = 1 },
		"carrier without eligibility": func(c *config.ExtensionsConfig) { c.CarrierEnabled = true },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if validateProtectedExtensionSettings(c) == nil {
				t.Fatal("incomplete protected release accepted")
			}
		})
	}
	base.CarrierEnabled = true
	base.RequiredInbound = "directory"
	if err := validateProtectedExtensionSettings(base); err != nil {
		t.Fatal(err)
	}
}
