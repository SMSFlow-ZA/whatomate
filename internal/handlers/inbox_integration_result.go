package handlers

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
)

// The same selection/validation is used for provider responses and manual
// attestations. Only declared typed values are retained, never an error body.
func selectInboxIntegrationResult(d models.InboxIntegration, fields map[uuid.UUID]models.InboxFieldDefinition, input map[string]json.RawMessage, credential string, strict bool) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > inboxintegration.MaxBody || len(input) > 64 {
		return nil, errInboxTransition
	}
	allowed := map[string]bool{}
	selected := map[string]json.RawMessage{}
	for _, output := range d.Outputs {
		allowed[output.Name] = true
		value, ok := input[output.Name]
		field, exists := fields[output.DefinitionID]
		if !ok || !exists || field.ValidateValue(value) != nil || emptyInboxField(value) || inboxIntegrationResultContainsCredential(value, credential) {
			return nil, errInboxTransition
		}
		selected[output.Name] = append(json.RawMessage{}, value...)
	}
	if d.ReferenceKey != "" {
		allowed[d.ReferenceKey] = true
		value, ok := input[d.ReferenceKey]
		var reference string
		if !ok || json.Unmarshal(value, &reference) != nil || strings.TrimSpace(reference) == "" || len(reference) > 200 || strings.ContainsAny(reference, "\r\n\x00") || inboxIntegrationResultContainsCredential(value, credential) {
			return nil, errInboxTransition
		}
		selected[d.ReferenceKey] = append(json.RawMessage{}, value...)
	}
	if strict {
		for name := range input {
			if !allowed[name] {
				return nil, errInboxTransition
			}
		}
	}
	return selected, nil
}
