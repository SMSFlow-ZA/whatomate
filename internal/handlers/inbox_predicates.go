package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// User predicates never contain SQL identifiers or nested expressions. Mandatory
// organization/account/team authorization remains outside this condition group.
func inboxPredicateSQL(db *gorm.DB, org uuid.UUID, p models.InboxPredicate, allowContact bool) (string, []any, error) {
	op := p.Operator
	if op == "" {
		op = "eq"
	}
	if p.Field == "custom_field" {
		if p.DefinitionID == nil {
			return "", nil, errInboxTransition
		}
		var d models.InboxFieldDefinition
		if err := db.Session(&gorm.Session{NewDB: true}).First(&d, "id = ? AND organization_id = ? AND (scope = 'case' OR ?)", p.DefinitionID, org, allowContact).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", nil, errInboxTransition
			}
			return "", nil, err
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
		if len(p.Value) == 0 || len(p.Value) > 32768 || d.ValidateValue(p.Value) != nil || emptyInboxField(p.Value) {
			return "", nil, errInboxTransition
		}
		comparison := map[string]string{"eq": "=", "not_eq": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[op]
		if op == "contains" && d.Kind == "text" {
			var value string
			if json.Unmarshal(p.Value, &value) != nil {
				return "", nil, errInboxTransition
			}
			return base + " AND position(lower(?) in lower(v.value #>> '{}')) > 0)", []any{d.ID, value}, nil
		}
		if comparison == "" {
			return "", nil, errInboxTransition
		}
		if op == "eq" || op == "not_eq" {
			return base + " AND v.value " + comparison + " ?::jsonb)", []any{d.ID, string(p.Value)}, nil
		}
		switch d.Kind {
		case "number":
			var value float64
			if json.Unmarshal(p.Value, &value) != nil {
				return "", nil, errInboxTransition
			}
			return base + " AND CASE WHEN jsonb_typeof(v.value) = 'number' THEN (v.value #>> '{}')::numeric END " + comparison + " ?)", []any{d.ID, value}, nil
		case "date":
			var value string
			if json.Unmarshal(p.Value, &value) != nil {
				return "", nil, errInboxTransition
			}
			return base + " AND (v.value #>> '{}')::date " + comparison + " ?::date)", []any{d.ID, value}, nil
		default:
			return "", nil, errInboxTransition
		}
	}
	var value string
	if json.Unmarshal(p.Value, &value) != nil || len(value) > 4096 {
		return "", nil, errInboxTransition
	}
	switch p.Field {
	case "status", "priority":
		if op != "eq" && op != "not_eq" {
			return "", nil, errInboxTransition
		}
		if p.Field == "status" {
			if !validateInboxFilter(models.InboxQueueFilter{Status: models.InboxCaseStatus(value)}) || value == "" {
				return "", nil, errInboxTransition
			}
		} else if !(inboxCaseUpdate{Priority: models.InboxCasePriority(value)}).valid(time.Time{}) || value == "" {
			return "", nil, errInboxTransition
		}
		comparison := "="
		if op == "not_eq" {
			comparison = "<>"
		}
		return "inbox_cases." + p.Field + " " + comparison + " ?", []any{value}, nil
	case "tag":
		if (op != "eq" && op != "not_eq") || strings.TrimSpace(value) == "" || len(value) > 100 {
			return "", nil, errInboxTransition
		}
		payload, _ := json.Marshal([]string{value})
		query := "COALESCE(c.tags, '[]'::jsonb) @> ?::jsonb"
		if op == "not_eq" {
			query = "NOT (" + query + ")"
		}
		return query, []any{string(payload)}, nil
	default:
		return "", nil, errInboxTransition
	}
}

func applyInboxPredicates(db, q *gorm.DB, org uuid.UUID, f models.InboxQueueFilter, allowContact bool) (*gorm.DB, error) {
	if len(f.Predicates) > 20 || (f.Match != "" && f.Match != "all" && f.Match != "any") {
		return nil, errInboxTransition
	}
	if len(f.Predicates) == 0 {
		return q, nil
	}
	parts := make([]string, 0, len(f.Predicates))
	var args []any
	for _, p := range f.Predicates {
		sql, values, err := inboxPredicateSQL(db, org, p, allowContact)
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

func validateInboxPredicates(db *gorm.DB, org uuid.UUID, f models.InboxQueueFilter, allowContact bool) error {
	_, err := applyInboxPredicates(db, db.Model(&models.InboxCase{}), org, f, allowContact)
	if err != nil {
		return fmt.Errorf("%w: invalid or unavailable condition", errInboxTransition)
	}
	return nil
}
