package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
)

func TestInboxIntakeTypedAnswersUseExactGrammarWithoutGuessing(t *testing.T) {
	for _, test := range []struct {
		kind     string
		input    any
		expected string
		valid    bool
	}{
		{"number", "0", "0", true}, {"number", "-1.25", "-1.25", true}, {"number", "1e3", "1e3", true},
		{"number", "1,000", "", false}, {"number", "01", "", false}, {"number", "+1", "", false}, {"number", "1e1000", "", false}, {"number", " 4 ", "", false}, {"number", false, "false", false},
		{"boolean", "false", "false", true}, {"boolean", "true", "true", true}, {"boolean", false, "false", true}, {"boolean", "yes", "", false}, {"boolean", 0, "0", false},
		{"text", "001", `"001"`, true}, {"email", "synthetic@example.invalid", `"synthetic@example.invalid"`, true},
		{"url", "http://example.invalid", `"http://example.invalid"`, false}, {"url", "https://example.invalid/path", `"https://example.invalid/path"`, true},
	} {
		t.Run(test.kind+"/"+test.expected, func(t *testing.T) {
			d := models.InboxFieldDefinition{Kind: test.kind}
			raw, err := intakeAnswerValue(d, test.input)
			if test.valid {
				require.NoError(t, err)
				require.NoError(t, d.ValidateValue(raw))
				require.JSONEq(t, test.expected, string(raw))
			} else {
				require.True(t, err != nil || d.ValidateValue(raw) != nil)
			}
		})
	}
}
