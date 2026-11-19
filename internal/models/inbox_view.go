package models

import (
	"encoding/json"

	"github.com/google/uuid"
)

type InboxFieldMatch struct {
	DefinitionID uuid.UUID       `json:"definition_id"`
	Value        json.RawMessage `json:"value"`
}

type InboxQueueFilter struct {
	Fields     []InboxFieldMatch `json:"fields,omitempty"`
	Queue      string            `json:"queue,omitempty"`
	AccountID  *uuid.UUID        `json:"account_id,omitempty"`
	TeamID     *uuid.UUID        `json:"team_id,omitempty"`
	AssigneeID *uuid.UUID        `json:"assignee_id,omitempty"`
	Status     InboxCaseStatus   `json:"status,omitempty"`
	Priority   InboxCasePriority `json:"priority,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
}

type InboxSavedView struct {
	BaseModel
	OrganizationID uuid.UUID        `gorm:"type:uuid;not null;index" json:"-"`
	OwnerID        uuid.UUID        `gorm:"type:uuid;not null;index" json:"owner_id"`
	TeamID         *uuid.UUID       `gorm:"type:uuid;index" json:"team_id,omitempty"`
	Name           string           `gorm:"size:100;not null" json:"name"`
	Filters        InboxQueueFilter `gorm:"serializer:json;type:jsonb;not null" json:"filters"`
}
