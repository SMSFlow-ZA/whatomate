package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"strings"
)

// Apply the organisation's current phone-display policy to every read surface,
// including CSV. Work on copies so historical evidence is never rewritten.
func maskAuditPhones(changes models.JSONBArray) models.JSONBArray {
	result := make(models.JSONBArray, len(changes))
	for i, raw := range changes {
		change, ok := raw.(map[string]any)
		if !ok {
			result[i] = map[string]any{"field": "changes_unavailable", "new_value": "[unavailable]"}
			continue
		}
		field, _ := change["field"].(string)
		phone := strings.Contains(strings.ToLower(field), "phone")
		copy := map[string]any{}
		for key, value := range change {
			copy[key] = maskAuditPhoneValue(value, phone && (key == "old_value" || key == "new_value"))
		}
		result[i] = copy
	}
	return result
}

func maskAuditPhoneValue(value any, phone bool) any {
	switch typed := value.(type) {
	case string:
		if typed == "[redacted]" || typed == "[truncated]" || typed == "[unavailable]" {
			return typed
		}
		if phone {
			return utils.MaskPhoneNumber(typed)
		}
		return utils.MaskIfPhoneNumber(typed)
	case map[string]any:
		copy := map[string]any{}
		for key, child := range typed {
			copy[key] = maskAuditPhoneValue(child, phone || strings.Contains(strings.ToLower(key), "phone"))
		}
		return copy
	case []any:
		copy := make([]any, len(typed))
		for i, child := range typed {
			copy[i] = maskAuditPhoneValue(child, phone)
		}
		return copy
	default:
		if phone && value != nil {
			return "[phone masked]"
		}
		return value
	}
}
