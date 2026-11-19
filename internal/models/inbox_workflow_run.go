package models

import (
	"time"

	"github.com/google/uuid"
)

// InboxWorkflowRun is also the outbound work queue. A submitting run is
// reconciled against its message ID after interruption, never submitted again.
type InboxWorkflowRun struct {
	BaseModel
	OrganizationID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	WorkflowID          uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:inbox_rule_event,priority:1" json:"workflow_id"`
	EventID             *uuid.UUID `gorm:"type:uuid;uniqueIndex:inbox_rule_event,priority:2" json:"event_id,omitempty"`
	CaseID              uuid.UUID  `gorm:"type:uuid;not null;index" json:"case_id"`
	AccountID           uuid.UUID  `gorm:"type:uuid;not null" json:"-"`
	ContactID           uuid.UUID  `gorm:"type:uuid;not null" json:"-"`
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
