package models

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"time"
)

type InboxSLAPolicy struct {
	OrganizationID    uuid.UUID                `gorm:"type:uuid;primaryKey" json:"-"`
	AccountID         uuid.UUID                `gorm:"type:uuid;primaryKey" json:"account_id"`
	VersionID         *uuid.UUID               `gorm:"type:uuid" json:"version_id,omitempty"`
	FirstReplyMinutes int                      `json:"first_reply_minutes"`
	NextReplyMinutes  int                      `json:"next_reply_minutes"`
	ResolutionMinutes int                      `json:"resolution_minutes"`
	Timezone          string                   `json:"timezone"`
	BusinessHours     bool                     `json:"business_hours"`
	Shifts            []inboxsla.Shift         `gorm:"serializer:json;type:jsonb" json:"shifts"`
	DateExceptions    []inboxsla.DateException `gorm:"serializer:json;type:jsonb" json:"date_exceptions"`
}

// InboxSLAVersion is an immutable policy assigned on entry to the human queue.
type InboxSLAVersion struct {
	BaseModel
	OrganizationID uuid.UUID                `gorm:"type:uuid;not null;index" json:"-"`
	AccountID      uuid.UUID                `gorm:"type:uuid;not null;index" json:"account_id"`
	Policy         InboxSLAPolicy           `gorm:"serializer:json;type:jsonb;not null" json:"policy"`
	Legacy         bool                     `gorm:"not null" json:"legacy"`
	Selection      *InboxResponseDefinition `gorm:"serializer:json;type:jsonb" json:"selection,omitempty"`
}

// Completed reply cycles remain available after the next cycle starts.
type InboxSLAObligation struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;index:idx_inbox_sla_due,priority:1;uniqueIndex:idx_inbox_sla_cycle,priority:1" json:"-"`
	AccountID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"account_id"`
	CaseID         uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:idx_inbox_sla_cycle,priority:2" json:"case_id"`
	PolicyID       uuid.UUID  `gorm:"type:uuid;not null;index" json:"policy_id"`
	Kind           string     `gorm:"size:16;not null;uniqueIndex:idx_inbox_sla_cycle,priority:3" json:"kind"`
	StartedAt      time.Time  `gorm:"not null;uniqueIndex:idx_inbox_sla_cycle,priority:4" json:"started_at"`
	Deadline       *time.Time `gorm:"index:idx_inbox_sla_due,priority:3" json:"deadline,omitempty"`
	AtRiskAt       *time.Time `gorm:"index" json:"at_risk_at,omitempty"`
	CompletedAt    *time.Time `gorm:"index:idx_inbox_sla_due,priority:2" json:"completed_at,omitempty"`
	Paused         bool       `gorm:"not null" json:"paused"`
	Outcome        string     `gorm:"size:16;not null" json:"outcome,omitempty"`
	WorkingSeconds int64      `gorm:"not null" json:"working_seconds"`
	BudgetMinutes  int        `gorm:"not null" json:"budget_minutes"`
	TeamID         *uuid.UUID `gorm:"type:uuid;index" json:"team_id,omitempty"`
	AgentID        *uuid.UUID `gorm:"type:uuid;index" json:"agent_id,omitempty"`
	Legacy         bool       `gorm:"not null" json:"legacy"`
}
