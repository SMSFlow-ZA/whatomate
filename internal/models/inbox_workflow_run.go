package models

import (
	"time"

	"github.com/google/uuid"
)

// InboxWorkflowRun is also the outbound work queue. A submitting run is
// reconciled against its message ID after interruption, never submitted again.
type InboxWorkflowRun struct {
	ConditionOutcomes []InboxConditionOutcome `gorm:"serializer:json;type:jsonb" json:"condition_outcomes,omitempty"`
	ActionOutcomes    []InboxActionOutcome    `gorm:"serializer:json;type:jsonb" json:"action_outcomes,omitempty"`
	ConditionsMatched *bool                   `json:"conditions_matched,omitempty"`
	BaseModel
	OrganizationID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	WorkflowID          uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:inbox_rule_event,priority:1" json:"workflow_id"`
	EventID             *uuid.UUID `gorm:"type:uuid;uniqueIndex:inbox_rule_event,priority:2" json:"event_id,omitempty"`
	CaseID              uuid.UUID  `gorm:"type:uuid;not null;index" json:"case_id"`
	AccountID           uuid.UUID  `gorm:"type:uuid;not null" json:"account_id"`
	ContactID           uuid.UUID  `gorm:"type:uuid;not null" json:"contact_id"`
	ActorID             uuid.UUID  `gorm:"type:uuid;not null" json:"actor_id"`
	Kind                string     `gorm:"size:10;not null" json:"kind"`
	WorkflowName        string     `gorm:"size:100;not null" json:"workflow_name"`
	WorkflowUpdatedAt   time.Time  `gorm:"not null" json:"-"`
	State               string     `gorm:"size:24;not null;index" json:"state"`
	Outcome             string     `gorm:"size:50" json:"outcome,omitempty"`
	SendDigest          string     `gorm:"size:64" json:"-"`
	CaseDigest          string     `gorm:"size:64" json:"-"`
	SubmissionStartedAt *time.Time `json:"submission_started_at,omitempty"`
}

type InboxActionOutcome struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

type InboxConditionOutcome struct {
	Field   string `json:"field"`
	Label   string `json:"label,omitempty"`
	Group   string `json:"group"`
	Matched bool   `json:"matched"`
}
