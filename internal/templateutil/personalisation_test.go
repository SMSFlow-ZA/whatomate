package templateutil_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/stretchr/testify/require"
)

func TestRenderStoredTemplatePreservesLiteralParameterValues(t *testing.T) {
	content := "Hello {{1}}; status {{2}}; again {{1}}"
	got := templateutil.ReplaceWithStringParams(content, map[string]string{"1": "Zoë {{2}}", "2": "Open"})
	require.Equal(t, "Hello Zoë {{2}}; status Open; again Zoë {{2}}", got)
	got = templateutil.ReplaceWithStringParams("{{ customer }} / {{unknown}}", map[string]string{"1": "Example"})
	require.Equal(t, "Example / {{unknown}}", got)
}
