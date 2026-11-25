package models

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// InboxFieldStamp retains a mutation counter even when a field is cleared.
// Intake snapshots can therefore detect an edit-and-clear (ABA) sequence.
// It contains no answer data and is never returned through a field-value API.
type InboxFieldStamp struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	DefinitionID   uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	SubjectID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	Revision       int64     `gorm:"not null;default:1" json:"-"`
}

// Mappings contain schema only. Customer answers live solely in encrypted,
// short-lived binding staging, then ordinary permission-scoped field values.
type InboxIntakeMapping struct {
	BaseModel
	OrganizationID uuid.UUID          `gorm:"type:uuid;not null;index" json:"-"`
	OwnerID        uuid.UUID          `gorm:"type:uuid;not null" json:"-"`
	Name           string             `gorm:"size:100;not null" json:"name"`
	Description    string             `gorm:"size:1000;not null;default:''" json:"description"`
	Revision       int64              `gorm:"not null;default:1" json:"revision"`
	Enabled        bool               `gorm:"not null;default:false" json:"enabled"`
	ConsentKey     string             `gorm:"size:64;not null" json:"consent_key"`
	Fields         []InboxIntakeInput `gorm:"serializer:json;type:jsonb;not null" json:"fields"`
}

type InboxIntakeInput struct {
	Input        string    `json:"input"`
	DefinitionID uuid.UUID `json:"definition_id"`
	Required     bool      `json:"required"`
}

func InboxIntakeKey(value string) bool {
	if value == "" || len(value) > 64 || value == "flow_token" {
		return false
	}
	for i, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (i > 0 && ((ch >= '0' && ch <= '9') || ch == '_')) {
			continue
		}
		return false
	}
	return true
}

func (m InboxIntakeMapping) Validate() error {
	if strings.TrimSpace(m.Name) == "" || utf8.RuneCountInString(m.Name) > 100 || utf8.RuneCountInString(m.Description) > 1000 || !InboxIntakeKey(m.ConsentKey) || len(m.Fields) == 0 || len(m.Fields) > 20 {
		return errors.New("provide a name, consent key and one to twenty intake fields")
	}
	inputs, definitions := map[string]bool{}, map[uuid.UUID]bool{}
	for _, field := range m.Fields {
		if !InboxIntakeKey(field.Input) || field.DefinitionID == uuid.Nil || inputs[field.Input] || definitions[field.DefinitionID] {
			return errors.New("choose unique named inputs and field definitions")
		}
		inputs[field.Input], definitions[field.DefinitionID] = true, true
	}
	return nil
}

type InboxIntakeFieldSnapshot struct {
	DefinitionID        uuid.UUID `json:"definition_id"`
	DefinitionRevision  int64     `json:"definition_revision"`
	DefinitionUpdatedAt time.Time `json:"definition_updated_at"`
	Scope               string    `json:"scope"`
	Stamp               int64     `json:"stamp"`
}

type InboxIntakeOutcome struct {
	DefinitionID uuid.UUID `json:"definition_id"`
	Status       string    `json:"status"`
}

type InboxIntakeBinding struct {
	BaseModel
	OrganizationID       uuid.UUID                  `gorm:"type:uuid;not null;index:idx_intake_session,priority:1;uniqueIndex:idx_intake_response,priority:1" json:"-"`
	AccountID            uuid.UUID                  `gorm:"type:uuid;not null" json:"-"`
	ContactID            uuid.UUID                  `gorm:"type:uuid;not null" json:"-"`
	SessionID            uuid.UUID                  `gorm:"type:uuid;not null;index:idx_intake_session,priority:2" json:"-"`
	FlowID               uuid.UUID                  `gorm:"type:uuid;not null" json:"-"`
	FlowDigest           string                     `gorm:"size:64;not null" json:"-"`
	NodeID               string                     `gorm:"size:100;not null" json:"-"`
	MappingID            uuid.UUID                  `gorm:"type:uuid;not null" json:"-"`
	MappingRevision      int64                      `gorm:"not null" json:"-"`
	PreviousCaseID       *uuid.UUID                 `gorm:"type:uuid" json:"-"`
	PreviousCaseVersion  int64                      `gorm:"not null;default:0" json:"-"`
	PreviousCaseResolved bool                       `gorm:"not null;default:false" json:"-"`
	Snapshots            []InboxIntakeFieldSnapshot `gorm:"serializer:json;type:jsonb;not null" json:"-"`
	State                string                     `gorm:"size:24;not null;index:idx_intake_pending,priority:1" json:"state"`
	IssuedAt             time.Time                  `gorm:"not null" json:"issued_at"`
	ExpiresAt            time.Time                  `gorm:"not null;index:idx_intake_pending,priority:2" json:"-"`
	ResponseKey          *uuid.UUID                 `gorm:"type:uuid;uniqueIndex:idx_intake_response,priority:2" json:"-"`
	ResponseAt           *time.Time                 `json:"-"`
	ConsentConfirmed     bool                       `gorm:"not null;default:false" json:"-"`
	CaseID               *uuid.UUID                 `gorm:"type:uuid;index" json:"case_id,omitempty"`
	EncryptedAnswers     string                     `gorm:"type:text;not null;default:''" json:"-"`
	Outcomes             []InboxIntakeOutcome       `gorm:"serializer:json;type:jsonb" json:"outcomes"`
}
