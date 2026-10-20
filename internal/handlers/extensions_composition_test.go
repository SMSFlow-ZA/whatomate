package handlers_test

import (
	"encoding/base64"
	"testing"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCompiledDirectoryCaseKnowledgeAIComposition(t *testing.T) {
	app := newTestApp(t)
	app.Config.Extensions = config.ExtensionsConfig{SecurityKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Modules: "directory,cases,knowledge,ai", RosterFreshnessSeconds: 86400, SyntheticDevelopment: true, DevelopmentAIEndpoint: "http://127.0.0.1:11434", DevelopmentModelDigest: ai.DevelopmentModelDigest, DevelopmentEmbeddingDigest: ai.DevelopmentEmbeddingDigest}
	require.NoError(t, app.InitializeExtensions())
	for _, id := range []string{"directory", "cases", "knowledge", "ai"} {
		require.NotNil(t, app.ExtensionAPI[id], id)
	}
	require.Len(t, app.ExtensionRegistry.Manifest(), 5)
}
