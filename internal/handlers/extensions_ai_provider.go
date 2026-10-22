package handlers

import (
	"fmt"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/shridarpatil/whatomate/internal/config"
)

type extensionAIProvider interface {
	ai.Generator
	ai.Embedder
	ai.Classifier
}

type extensionAIProviderConfig struct {
	Provider             string
	SyntheticDevelopment bool
	Development          ai.DevelopmentConfig
	Production           ai.ProviderConfig
	Embedding            ai.EmbeddingConfig
}

// Both operator assistance and employee conversations must use the same explicit
// provider selection. Missing production configuration cannot select local AI.
func extensionAIProviderConfigFromHost(c config.ExtensionsConfig) extensionAIProviderConfig {
	return extensionAIProviderConfig{
		Provider: c.AIProvider, SyntheticDevelopment: c.SyntheticDevelopment,
		Development: ai.DevelopmentConfig{SyntheticDevelopment: c.SyntheticDevelopment, Endpoint: c.DevelopmentAIEndpoint, ModelDigest: c.DevelopmentModelDigest, EmbeddingDigest: c.DevelopmentEmbeddingDigest, Timeout: 110 * time.Second},
		Production:  ai.ProviderConfig{Endpoint: c.AIEndpoint, AllowedEndpoints: c.AIAllowedEndpoints, DeploymentAttestation: c.AIDeploymentAttestation, Country: c.AICountry, Model: c.AIModel, PromptRevision: c.AIPromptRevision, APIKey: c.AIAPIKey, ProcessingScope: c.AIProcessingScope, InternationalProcessingApproval: c.AIInternationalProcessingApproval, InternationalProcessingDisclosure: c.AIInternationalProcessingDisclosure, Timeout: 60 * time.Second, MaxResponseBytes: 1024 * 1024},
		Embedding:   ai.EmbeddingConfig{Endpoint: c.AIEmbeddingEndpoint, Model: c.AIEmbeddingModel, Dimensions: c.AIEmbeddingDimensions},
	}
}

func newExtensionAIProvider(c extensionAIProviderConfig) (extensionAIProvider, error) {
	switch c.Provider {
	case "https":
		if c.SyntheticDevelopment {
			return nil, fmt.Errorf("production AI cannot use synthetic development mode")
		}
		return ai.NewProductionProvider(c.Production, c.Embedding)
	case "development", "":
		if !c.SyntheticDevelopment {
			return nil, fmt.Errorf("AI requires an explicit reviewed HTTPS provider")
		}
		c.Development.SyntheticDevelopment = true
		return ai.NewDevelopmentProvider(c.Development)
	default:
		return nil, fmt.Errorf("unsupported extension AI provider")
	}
}
