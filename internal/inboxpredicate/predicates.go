// Package inboxpredicate compiles bounded typed conditions, without HTTP or
// authorisation side effects. Callers must apply mandatory access scopes outside
// the returned all/any group and use the existing contact alias c for tag tests.
package inboxpredicate

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// ErrInvalid identifies malformed or unavailable condition inputs, without
// exposing whether a missing field belongs to another organisation.
var ErrInvalid = errors.New("invalid or unavailable inbox condition")

// Definitions loads only the requested organisation's field definitions.
// allowContact must reflect the caller's existing contact-read permission.
func Definitions(db *gorm.DB, org uuid.UUID, predicates []models.InboxPredicate, allowContact bool) (map[uuid.UUID]models.InboxFieldDefinition, error) {
	ids := []uuid.UUID{}
	seen := map[uuid.UUID]bool{}
	for _, p := range predicates {
		if p.Field != "custom_field" {
			continue
		}
		if p.DefinitionID == nil {
			return nil, ErrInvalid
		}
		if !seen[*p.DefinitionID] {
			ids = append(ids, *p.DefinitionID)
			seen[*p.DefinitionID] = true
		}
	}
	result := map[uuid.UUID]models.InboxFieldDefinition{}
	if len(ids) == 0 {
		return result, nil
	}
	var definitions []models.InboxFieldDefinition
	if err := db.Session(&gorm.Session{NewDB: true}).Where("organization_id=? AND id IN ? AND (scope='case' OR ?)", org, ids, allowContact).Find(&definitions).Error; err != nil {
		return nil, err
	}
	if len(definitions) != len(ids) {
		return nil, ErrInvalid
	}
	for _, d := range definitions {
		result[d.ID] = d
	}
	return result, nil
}

// SQL loads definitions and compiles one condition. It does not scope a case query.
func SQL(db *gorm.DB, org uuid.UUID, p models.InboxPredicate, allowContact bool) (string, []any, error) {
	definitions, err := Definitions(db, org, []models.InboxPredicate{p}, allowContact)
	if err != nil {
		return "", nil, err
	}
	return Compile(p, definitions)
}

// Compile returns a fixed SQL expression and bound values, never caller-supplied
// identifiers, nested expressions or regular expressions.
func Compile(p models.InboxPredicate, definitions map[uuid.UUID]models.InboxFieldDefinition) (string, []any, error) {
	op := p.Operator
	if op == "" {
		op = "eq"
	}
	if p.Field == "custom_field" {
		if p.DefinitionID == nil {
			return "", nil, ErrInvalid
		}
		d, ok := definitions[*p.DefinitionID]
		if !ok {
			return "", nil, ErrInvalid
		}
		base := "EXISTS (SELECT 1 FROM inbox_field_values v WHERE v.organization_id = inbox_cases.organization_id AND v.definition_id = ? AND v.subject_id = "
		subject := "inbox_cases.id"
		if d.Scope == "contact" {
			subject = "inbox_cases.contact_id"
		}
		base += subject
		if op == "is_empty" || op == "not_empty" {
			base += " AND v.value <> 'null'::jsonb AND v.value <> '\"\"'::jsonb)"
			if op == "is_empty" {
				base = "NOT " + base
			}
			return base, []any{d.ID}, nil
		}
		if len(p.Value) == 0 || len(p.Value) > 32768 || d.ValidateValue(p.Value) != nil || empty(p.Value) {
			return "", nil, ErrInvalid
		}
		comparison := map[string]string{"eq": "=", "not_eq": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[op]
		if op == "contains" && d.Kind == "text" {
			var value string
			if json.Unmarshal(p.Value, &value) != nil {
				return "", nil, ErrInvalid
			}
			return base + " AND position(lower(?) in lower(v.value #>> '{}')) > 0)", []any{d.ID, value}, nil
		}
		if comparison == "" {
			return "", nil, ErrInvalid
		}
		if op == "eq" || op == "not_eq" {
			return base + " AND v.value " + comparison + " ?::jsonb)", []any{d.ID, string(p.Value)}, nil
		}
		switch d.Kind {
		case "number":
			var value float64
			if json.Unmarshal(p.Value, &value) != nil {
				return "", nil, ErrInvalid
			}
			return base + " AND CASE WHEN jsonb_typeof(v.value) = 'number' THEN (v.value #>> '{}')::numeric END " + comparison + " ?)", []any{d.ID, value}, nil
		case "date":
			var value string
			if json.Unmarshal(p.Value, &value) != nil {
				return "", nil, ErrInvalid
			}
			return base + " AND (v.value #>> '{}')::date " + comparison + " ?::date)", []any{d.ID, value}, nil
		default:
			return "", nil, ErrInvalid
		}
	}
	var value string
	if json.Unmarshal(p.Value, &value) != nil || len(value) > 4096 {
		return "", nil, ErrInvalid
	}
	switch p.Field {
	case "status", "priority":
		if op != "eq" && op != "not_eq" {
			return "", nil, ErrInvalid
		}
		if p.Field == "status" {
			if !slices.Contains([]models.InboxCaseStatus{models.InboxCaseOpen, models.InboxCasePending, models.InboxCaseWaiting, models.InboxCaseSnoozed, models.InboxCaseResolved}, models.InboxCaseStatus(value)) {
				return "", nil, ErrInvalid
			}
		} else if !slices.Contains([]models.InboxCasePriority{models.InboxPriorityLow, models.InboxPriorityNormal, models.InboxPriorityHigh, models.InboxPriorityUrgent}, models.InboxCasePriority(value)) {
			return "", nil, ErrInvalid
		}
		comparison := "="
		if op == "not_eq" {
			comparison = "<>"
		}
		return "inbox_cases." + p.Field + " " + comparison + " ?", []any{value}, nil
	case "tag":
		if (op != "eq" && op != "not_eq") || strings.TrimSpace(value) == "" || len(value) > 100 {
			return "", nil, ErrInvalid
		}
		payload, _ := json.Marshal([]string{value})
		query := "COALESCE(c.tags, '[]'::jsonb) @> ?::jsonb"
		if op == "not_eq" {
			query = "NOT (" + query + ")"
		}
		return query, []any{string(payload)}, nil
	default:
		return "", nil, ErrInvalid
	}
}

// Apply adds a bounded all/any predicate group. It intentionally does not apply
// other queue filters or grant access: q must already have mandatory scopes.
func Apply(db, q *gorm.DB, org uuid.UUID, f models.InboxQueueFilter, allowContact bool) (*gorm.DB, error) {
	if len(f.Predicates) > 20 || (f.Match != "" && f.Match != "all" && f.Match != "any") {
		return nil, ErrInvalid
	}
	if len(f.Predicates) == 0 {
		return q, nil
	}
	definitions, err := Definitions(db, org, f.Predicates, allowContact)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(f.Predicates))
	var args []any
	for _, p := range f.Predicates {
		sql, values, err := Compile(p, definitions)
		if err != nil {
			return nil, err
		}
		parts = append(parts, "("+sql+")")
		args = append(args, values...)
	}
	join := " AND "
	if f.Match == "any" {
		join = " OR "
	}
	return q.Where("("+strings.Join(parts, join)+")", args...), nil
}

func empty(value json.RawMessage) bool {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return true
	}
	var text string
	return json.Unmarshal(value, &text) == nil && strings.TrimSpace(text) == ""
}
