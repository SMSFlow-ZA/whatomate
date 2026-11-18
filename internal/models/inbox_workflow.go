package models

import (
	"time"

	"github.com/google/uuid"
)

type InboxAction struct {
	ConsentConfirmed bool   `json:"consent_confirmed,omitempty"`
	Type             string `json:"type"`
	Value            string `json:"value,omitempty"`
}

type InboxWorkflow struct {
	BaseModel
	OrganizationID uuid.UUID        `gorm:"type:uuid;not null;index" json:"-"`
	CreatedByID    uuid.UUID        `gorm:"type:uuid;not null" json:"created_by_id"`
	Name           string           `gorm:"size:100;not null" json:"name"`
	Kind           string           `gorm:"size:10;not null" json:"kind"`
	Trigger        string           `gorm:"size:24" json:"trigger,omitempty"`
	EnabledAt      *time.Time       `json:"enabled_at,omitempty"`
	Position       int              `gorm:"not null" json:"position"`
	Conditions     InboxQueueFilter `gorm:"type:jsonb;serializer:json;not null" json:"conditions"`
	Actions        []InboxAction    `gorm:"type:jsonb;serializer:json;not null" json:"actions"`
}
