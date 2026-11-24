// Package inboxtext renders bounded, non-executable inbox message variables.
// It has no access to a database, environment, filesystem or network.
package inboxtext

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MaxCharacters = 4096
	MaxVariables  = 64
)

var ErrInvalid = errors.New("invalid message personalisation")

type Value struct {
	Text   string
	Reason string // Empty means available. Unavailable values are never substituted.
}

type Missing struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type Result struct {
	Text    string    `json:"text"`
	Missing []Missing `json:"missing,omitempty"`
}

type part struct {
	literal, key, original string
	fallback               *string
}

// ValidKey is a deliberately closed grammar, not a general expression language.
func ValidKey(key string) bool {
	switch key {
	case "contact_name", "phone_number", "user_name", "agent_name", "account_name", "case_id", "case_status", "case_priority":
		return true
	}
	field := ""
	for _, prefix := range []string{"contact.field.", "case.field."} {
		if strings.HasPrefix(key, prefix) {
			field = strings.TrimPrefix(key, prefix)
			break
		}
	}
	if len(field) == 0 || len(field) > 64 {
		return false
	}
	for i, ch := range field {
		if ch >= 'a' && ch <= 'z' || i > 0 && (ch >= '0' && ch <= '9' || ch == '_') {
			continue
		}
		return false
	}
	return true
}

func parse(text string) ([]part, error) {
	if len(text) > 4*MaxCharacters || !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxCharacters {
		return nil, ErrInvalid
	}
	parts := []part{}
	count := 0
	for text != "" {
		start := strings.Index(text, "{{")
		if start < 0 {
			parts = append(parts, part{literal: text})
			break
		}
		parts = append(parts, part{literal: text[:start]})
		expression := text[start+2:]
		end, quoted, escaped := -1, false, false
		for i := 0; i < len(expression); i++ {
			ch := expression[i]
			if quoted {
				if escaped {
					escaped = false
				} else if ch == '\\' {
					escaped = true
				} else if ch == '"' {
					quoted = false
				}
			} else if ch == '"' {
				quoted = true
			} else if ch == '}' && i+1 < len(expression) && expression[i+1] == '}' {
				end = i
				break
			}
		}
		count++
		if end < 0 || count > MaxVariables {
			return nil, ErrInvalid
		}
		pieces := strings.SplitN(expression[:end], "|", 2)
		p := part{key: strings.TrimSpace(pieces[0]), original: text[start : start+2+end+2]}
		if !ValidKey(p.key) {
			return nil, ErrInvalid
		}
		if len(pieces) == 2 {
			var fallback *string
			if json.Unmarshal([]byte(strings.TrimSpace(pieces[1])), &fallback) != nil || fallback == nil || utf8.RuneCountInString(*fallback) > 256 {
				return nil, ErrInvalid
			}
			p.fallback = fallback
		}
		parts = append(parts, p)
		text = expression[end+2:]
	}
	return parts, nil
}

// Keys validates syntax and returns unique dependencies in authored order.
func Keys(text string) ([]string, error) {
	return KeysForTexts([]string{text})
}

// KeysForTexts applies the occurrence budget across all parts of one message,
// including separately authored template header/body parameter mappings.
func KeysForTexts(texts []string) ([]string, error) {
	keys, seen := []string{}, map[string]bool{}
	count := 0
	for _, text := range texts {
		parts, err := parse(text)
		if err != nil {
			return nil, err
		}
		for _, p := range parts {
			if p.key == "" {
				continue
			}
			count++
			if count > MaxVariables {
				return nil, ErrInvalid
			}
			if !seen[p.key] {
				keys = append(keys, p.key)
				seen[p.key] = true
			}
		}
	}
	return keys, nil
}

// Render substitutes once only. Substituted text is never parsed as a template.
// Unknown keys cannot be rescued by fallback text: the context must explicitly
// recognise a variable before an authored fallback can be used.
func Render(text string, values map[string]Value) (Result, error) {
	parts, err := parse(text)
	if err != nil {
		return Result{}, err
	}
	var out strings.Builder
	result, seen := Result{}, map[string]bool{}
	for _, p := range parts {
		if p.key == "" {
			out.WriteString(p.literal)
			continue
		}
		v, known := values[p.key]
		reason := v.Reason
		if reason == "" && strings.TrimSpace(v.Text) == "" {
			reason = "empty"
		}
		if known && reason == "" {
			out.WriteString(v.Text)
		} else if known && p.fallback != nil {
			out.WriteString(*p.fallback)
		} else {
			out.WriteString(p.original)
			if !known {
				reason = "unavailable"
			}
			if !seen[p.key] {
				result.Missing = append(result.Missing, Missing{Key: p.key, Reason: reason})
				seen[p.key] = true
			}
		}
		if out.Len() > 4*MaxCharacters {
			return Result{}, ErrInvalid
		}
	}
	result.Text = out.String()
	if !utf8.ValidString(result.Text) || utf8.RuneCountInString(result.Text) > MaxCharacters {
		return Result{}, ErrInvalid
	}
	return result, nil
}
