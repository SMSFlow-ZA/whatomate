package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/shridarpatil/whatomate/internal/config"
)

func TestExtensionAIProcessingDisclosureIsScopedCanonicalAndSecretFree(t *testing.T) {
	c := validProductionAIHostConfig()
	c.AIProcessingScope = "global_standard"
	c.AIInternationalProcessingApproval = "private-approval-id"
	c.AIInternationalProcessingDisclosure = "operator-provided private text"
	c.AIAPIKey = "private-key-value"
	a := &App{Config: &config.Config{Extensions: c}}
	actor := addonapi.Actor{Permissions: []string{"ai:assist"}}
	value, present := a.extensionCatalogAIProcessing(actor)
	if !present || value == nil {
		t.Fatal("configured international disclosure omitted")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{c.AIInternationalProcessingApproval, c.AIInternationalProcessingDisclosure, c.AIAPIKey, c.AIEndpoint, c.AIEmbeddingEndpoint} {
		if strings.Contains(text, secret) {
			t.Fatal("private provider configuration exposed")
		}
	}
	if !strings.Contains(text, "including embedding generation") || !strings.Contains(text, "configured for application data") {
		t.Fatal("truthful processing scope missing")
	}
	if _, present = a.extensionCatalogAIProcessing(addonapi.Actor{}); present {
		t.Fatal("disclosure exposed outside AI permission")
	}
	c.AIInternationalProcessingApproval = ""
	a.Config.Extensions = c
	if value, present = a.extensionCatalogAIProcessing(actor); !present || value != nil {
		t.Fatal("invalid international configuration silently hidden")
	}
	a.Config.Extensions = config.ExtensionsConfig{AIProcessingScope: "global_standard", AICountry: "ZA"}
	if value, present = a.extensionCatalogAIProcessing(actor); !present || value != nil {
		t.Fatal("country label alone certified processing location")
	}
}
