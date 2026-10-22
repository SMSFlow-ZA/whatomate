package handlers

import addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"

const extensionGlobalAIProcessingNotice = "This deployment is configured for application data and RAG storage in South Africa. Azure OpenAI Global Standard inference, including embedding generation, may process submitted content internationally."

type extensionAIProcessingDisclosure struct {
	Scope                        string `json:"scope"`
	StoredDataCountry            string `json:"stored_data_country"`
	InferenceMayBeInternational  bool   `json:"inference_may_be_international"`
	EmbeddingsMayBeInternational bool   `json:"embeddings_may_be_international"`
	Notice                       string `json:"notice"`
}

// This is a public deployment assertion, not proof of resource/data locality.
// Never serialize freeform operator disclosure, approval IDs or provider config.
// A present null means configuration is unavailable: the UI blocks new AI
// submissions without discarding the actor's unrelated authenticated authority.
func (a *App) extensionCatalogAIProcessing(actor addonapi.Actor) (any, bool) {
	if a == nil || a.Config == nil || !actor.Has("ai:assist") || a.Config.Extensions.AIProcessingScope != "global_standard" {
		return nil, false
	}
	c := a.Config.Extensions
	if c.AIProvider != "https" || c.SyntheticDevelopment {
		return nil, true
	}
	if _, err := newExtensionAIProvider(extensionAIProviderConfigFromHost(c)); err != nil {
		return nil, true
	}
	return extensionAIProcessingDisclosure{Scope: "global_standard", StoredDataCountry: "ZA", InferenceMayBeInternational: true, EmbeddingsMayBeInternational: true, Notice: extensionGlobalAIProcessingNotice}, true
}
