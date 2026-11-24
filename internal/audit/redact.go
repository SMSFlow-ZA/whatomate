package audit

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shridarpatil/whatomate/internal/models"
)

const redacted = "[redacted]"
const truncated = "[truncated]"

func sensitiveField(name string) bool {
	var key strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' {
			key.WriteRune(r)
		}
	}
	for _, secret := range []string{"password", "secret", "token", "credential", "authorization", "cookie", "apikey", "accesskey", "privatekey", "encryptionkey", "jwt", "bearer"} {
		if strings.Contains(key.String(), secret) {
			return true
		}
	}
	return false
}

type changeBudget struct{ nodes, bytes int }

func (b *changeBudget) text(value string, limit int) string {
	if !utf8.ValidString(value) {
		return "[unavailable]"
	}
	if limit > b.bytes {
		limit = b.bytes
	}
	if limit <= 0 {
		return truncated
	}
	if len(value) <= limit {
		b.bytes -= len(value)
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	b.bytes -= limit
	return value[:limit] + truncated
}

func (b *changeBudget) value(field string, value any, depth int) any {
	if value == nil {
		return nil
	}
	if sensitiveField(field) {
		return redacted
	}
	if depth > 4 || b.nodes <= 0 || b.bytes <= 0 {
		return truncated
	}
	b.nodes--
	switch v := value.(type) {
	case string:
		return b.text(v, 512)
	case bool:
		return v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "[unavailable]"
		}
		return v
	case json.Number:
		if n, err := v.Float64(); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
			return v
		}
		return "[unavailable]"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return v
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := map[string]any{}
		for i, key := range keys {
			if i >= 20 || b.nodes <= 0 || b.bytes <= 0 {
				result["_truncated"] = true
				break
			}
			result[b.text(key, 80)] = b.value(key, v[key], depth+1)
		}
		return result
	case []any:
		result := make([]any, 0, min(len(v), 20))
		for i, item := range v {
			if i >= 20 || b.nodes <= 0 || b.bytes <= 0 {
				result = append(result, truncated)
				break
			}
			result = append(result, b.value(field, item, depth+1))
		}
		return result
	default:
		// Do not invoke arbitrary MarshalJSON/String methods while redacting.
		return "[unavailable]"
	}
}

// SanitizeChanges protects both new diffs and older stored records. It copies
// bounded field/old/new data, drops extra payload keys and never mutates history.
func SanitizeChanges(changes models.JSONBArray) models.JSONBArray {
	result := make(models.JSONBArray, 0, min(len(changes), 64))
	budget := changeBudget{nodes: 256, bytes: 8192}
	for i, raw := range changes {
		if i >= 64 || budget.nodes <= 0 || budget.bytes <= 0 {
			result = append(result, map[string]any{"field": "changes_truncated", "old_value": nil, "new_value": truncated})
			break
		}
		change, ok := raw.(map[string]any)
		if !ok {
			result = append(result, map[string]any{"field": "changes_unavailable", "old_value": nil, "new_value": "[unavailable]"})
			continue
		}
		field, ok := change["field"].(string)
		if !ok || field == "" {
			result = append(result, map[string]any{"field": "changes_unavailable", "old_value": nil, "new_value": "[unavailable]"})
			continue
		}
		result = append(result, map[string]any{"field": budget.text(field, 80), "old_value": budget.value(field, change["old_value"], 0), "new_value": budget.value(field, change["new_value"], 0)})
	}
	return result
}
