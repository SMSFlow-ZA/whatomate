package handlers

import (
	"testing"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/shridarpatil/whatomate/internal/config"
)

func validProductionAIHostConfig() config.ExtensionsConfig {
	return config.ExtensionsConfig{AIProvider: "https", AIEndpoint: "https://example.invalid/chat", AIEmbeddingEndpoint: "https://example.invalid/embed", AIAllowedEndpoints: []string{"https://example.invalid/chat", "https://example.invalid/embed"}, AIDeploymentAttestation: "operator-reviewed-evidence", AICountry: "ZA", AIModel: "pinned-chat", AIEmbeddingModel: "pinned-embed", AIPromptRevision: "prompt-1", AIEmbeddingDimensions: 768}
}
func TestExtensionAIProductionSelectionFailsClosed(t *testing.T) {
	for _, mutate := range []func(*config.ExtensionsConfig){
		func(c *config.ExtensionsConfig) { c.AIProvider = "" },
		func(c *config.ExtensionsConfig) { c.AIProvider = "unknown" },
		func(c *config.ExtensionsConfig) { c.AIProvider = "development" },
		func(c *config.ExtensionsConfig) { c.SyntheticDevelopment = true },
		func(c *config.ExtensionsConfig) { c.AIDeploymentAttestation = "" },
		func(c *config.ExtensionsConfig) { c.AIEndpoint = "http://example.invalid/chat" },
		func(c *config.ExtensionsConfig) { c.AIEmbeddingEndpoint = "http://example.invalid/embed" },
		func(c *config.ExtensionsConfig) { c.AIAllowedEndpoints = nil },
		func(c *config.ExtensionsConfig) { c.AICountry = "US" },
	} {
		c := validProductionAIHostConfig()
		mutate(&c)
		if _, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(c)); err == nil {
			t.Fatalf("unsafe provider accepted: %q", c.AIProvider)
		}
	}
	p, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(validProductionAIHostConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*ai.ProductionProvider); !ok {
		t.Fatalf("production selected %T", p)
	}
}
func TestExtensionAIDevelopmentIsExplicitlySynthetic(t *testing.T) {
	c := config.ExtensionsConfig{SyntheticDevelopment: true, DevelopmentAIEndpoint: "http://127.0.0.1:11434", DevelopmentModelDigest: ai.DevelopmentModelDigest, DevelopmentEmbeddingDigest: ai.DevelopmentEmbeddingDigest}
	for _, selection := range []string{"", "development"} {
		c.AIProvider = selection
		p, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(c))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*ai.DevelopmentProvider); !ok {
			t.Fatalf("development selected %T", p)
		}
	}
	c.SyntheticDevelopment = false
	if _, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(c)); err == nil {
		t.Fatal("development enabled without synthetic flag")
	}
}

func TestExtensionAIInternationalProcessingRequiresExplicitApprovalAndDisclosure(t *testing.T) {
	c := validProductionAIHostConfig()
	c.AIProcessingScope = "global_standard"
	if _, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(c)); err == nil {
		t.Fatal("international processing silently enabled")
	}
	c.AIInternationalProcessingApproval = "operator-reviewed-approval"
	if _, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(c)); err == nil {
		t.Fatal("missing international disclosure accepted")
	}
	c.AIInternationalProcessingDisclosure = "Stored data remains in ZA; inference may process internationally."
	if _, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(c)); err != nil {
		t.Fatal(err)
	}
}
