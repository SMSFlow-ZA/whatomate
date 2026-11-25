package handlers

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/shridarpatil/whatomate/internal/models"
)

// Text replies can represent a typed number or true/false. Parse only the exact
// JSON grammar for that type: no locale guesses, truncation or truthiness.
// Consent itself is deliberately NOT passed through this conversion.
func intakeAnswerValue(d models.InboxFieldDefinition, value any) (json.RawMessage, error) {
	if text, ok := value.(string); ok {
		if len(text) > 32768 {
			return nil, errors.New("intake value too large")
		}
		if strings.TrimSpace(text) != "" && (d.Kind == "number" || d.Kind == "boolean") {
			if strings.TrimSpace(text) != text {
				return nil, errors.New("invalid typed intake value")
			}
			raw := json.RawMessage(text)
			if d.ValidateValue(raw) != nil {
				return nil, errors.New("invalid typed intake value")
			}
			return raw, nil
		}
	}
	return json.Marshal(value)
}
