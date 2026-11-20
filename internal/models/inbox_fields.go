package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type InboxFieldDefinition struct {
	BaseModel
	OrganizationID    uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_field_scope,priority:1" json:"-"`
	Scope             string     `gorm:"size:12;not null;uniqueIndex:idx_inbox_field_scope,priority:2" json:"scope"`
	Key               string     `gorm:"size:64;not null;uniqueIndex:idx_inbox_field_scope,priority:3" json:"key"`
	Description       string     `gorm:"size:1000;not null;default:''" json:"description"`
	Label             string     `gorm:"size:100;not null" json:"label"`
	Kind              string     `gorm:"size:16;not null" json:"kind"`
	Options           []string   `gorm:"serializer:json;type:jsonb" json:"options,omitempty"`
	RequiredOnResolve bool       `gorm:"not null;default:false" json:"required_on_resolve"`
	ArchivedAt        *time.Time `json:"archived_at,omitempty"`
}

type InboxFieldValue struct {
	OrganizationID uuid.UUID       `gorm:"type:uuid;primaryKey" json:"-"`
	DefinitionID   uuid.UUID       `gorm:"type:uuid;primaryKey" json:"definition_id"`
	SubjectID      uuid.UUID       `gorm:"type:uuid;primaryKey" json:"subject_id"`
	Value          json.RawMessage `gorm:"serializer:json;type:jsonb" json:"value"`
}

func (f InboxFieldDefinition) Validate() error {
	if utf8.RuneCountInString(f.Description) > 1000 {
		return errors.New("field description must be at most 1000 characters")
	}
	if f.Scope != "contact" && f.Scope != "case" {
		return errors.New("invalid field scope")
	}
	if f.Key == "" || len(f.Key) > 64 || strings.TrimSpace(f.Label) == "" || utf8.RuneCountInString(f.Label) > 100 {
		return errors.New("field key and label are required")
	}
	for i, ch := range f.Key {
		if (ch >= 'a' && ch <= 'z') || (i > 0 && ((ch >= '0' && ch <= '9') || ch == '_')) {
			continue
		}
		return errors.New("invalid field key")
	}
	switch f.Kind {
	case "text", "number", "date", "boolean":
		if len(f.Options) > 0 {
			return errors.New("options are only available for single select")
		}
	case "select":
		if len(f.Options) == 0 || len(f.Options) > 100 {
			return errors.New("provide between 1 and 100 choices")
		}
		seen := make(map[string]bool, len(f.Options))
		for _, option := range f.Options {
			if strings.TrimSpace(option) == "" || utf8.RuneCountInString(option) > 100 || seen[option] {
				return errors.New("select choices must be non-empty and unique")
			}
			seen[option] = true
		}
	default:
		return errors.New("invalid field type")
	}
	return nil
}

func (f InboxFieldDefinition) ValidateValue(value json.RawMessage) error {
	// Clearing a value is always allowed. Required fields are enforced by the
	// resolution transition when the case is ready for closure.
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil
	}
	if !json.Valid(value) {
		return errors.New("invalid field value")
	}
	switch f.Kind {
	case "text":
		var v string
		if json.Unmarshal(value, &v) == nil && utf8.RuneCountInString(v) <= 4096 {
			return nil
		}
	case "date":
		var v string
		if json.Unmarshal(value, &v) == nil {
			if _, err := time.Parse(time.DateOnly, v); err == nil {
				return nil
			}
		}
	case "number":
		var v float64
		if json.Unmarshal(value, &v) == nil && !math.IsInf(v, 0) && !math.IsNaN(v) {
			return nil
		}
	case "boolean":
		var v bool
		if json.Unmarshal(value, &v) == nil {
			return nil
		}
	case "select":
		var v string
		if json.Unmarshal(value, &v) == nil && slices.Contains(f.Options, v) {
			return nil
		}
	}
	return errors.New("field value does not match its type")
}
