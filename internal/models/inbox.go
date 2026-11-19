package models

import (
	"time"

	"github.com/google/uuid"
)

type InboxCaseStatus string

const (
	InboxCaseOpen     InboxCaseStatus = "open"
	InboxCasePending  InboxCaseStatus = "pending"
	InboxCaseWaiting  InboxCaseStatus = "waiting_customer"
	InboxCaseSnoozed  InboxCaseStatus = "snoozed"
	InboxCaseResolved InboxCaseStatus = "resolved"
)

type InboxCasePriority string

const (
	InboxPriorityLow    InboxCasePriority = "low"
	InboxPriorityNormal InboxCasePriority = "normal"
	InboxPriorityHigh   InboxCasePriority = "high"
	InboxPriorityUrgent InboxCasePriority = "urgent"
)

// InboxCase tracks an episode of human work on a WhatsApp thread. The thread
// and its message history remain attached to the existing contact and account.
type InboxCase struct {
	BaseModel
	OrganizationID       uuid.UUID         `gorm:"type:uuid;not null;index:idx_inbox_cases_thread,priority:1" json:"-"`
	AccountID            uuid.UUID         `gorm:"type:uuid;not null;index:idx_inbox_cases_thread,priority:2" json:"account_id"`
	ContactID            uuid.UUID         `gorm:"type:uuid;not null;index:idx_inbox_cases_thread,priority:3" json:"contact_id"`
	TeamID               *uuid.UUID        `gorm:"type:uuid;index" json:"team_id,omitempty"`
	RoutingReason        string            `gorm:"size:24;not null;default:''" json:"routing_reason,omitempty"`
	RoutingNextAttemptAt *time.Time        `json:"-"`
	AssigneeID           *uuid.UUID        `gorm:"type:uuid;index" json:"assignee_id,omitempty"`
	Status               InboxCaseStatus   `gorm:"size:24;not null" json:"status"`
	Priority             InboxCasePriority `gorm:"size:12;not null" json:"priority"`
	Version              int64             `gorm:"not null" json:"version"`
	OpenedAt             time.Time         `gorm:"not null;index:idx_inbox_cases_thread,priority:4,sort:desc" json:"opened_at"`
	StatusChangedAt      time.Time         `gorm:"not null" json:"status_changed_at"`
	SnoozedUntil         *time.Time        `json:"snoozed_until,omitempty"`
	ResolvedAt           *time.Time        `json:"resolved_at,omitempty"`
	LastInboundAt        *time.Time        `json:"last_inbound_at,omitempty"`
	FirstHumanReplyAt    *time.Time        `json:"first_human_reply_at,omitempty"`
	LastHumanReplyAt     *time.Time        `json:"last_human_reply_at,omitempty"`
}

type InboxCaseActivity struct {
	BaseModel
	OrganizationID uuid.UUID         `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_event,priority:1" json:"-"`
	AccountID      uuid.UUID         `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_event,priority:2" json:"-"`
	CaseID         uuid.UUID         `gorm:"type:uuid;not null;index" json:"case_id"`
	EventKey       *string           `gorm:"size:255;uniqueIndex:idx_inbox_event,priority:3" json:"-"`
	Detail         string            `gorm:"size:500" json:"detail,omitempty"`
	ActorID        *uuid.UUID        `gorm:"type:uuid" json:"actor_id,omitempty"`
	Kind           string            `gorm:"size:32;not null" json:"kind"`
	FromStatus     InboxCaseStatus   `gorm:"size:24" json:"from_status,omitempty"`
	ToStatus       InboxCaseStatus   `gorm:"size:24" json:"to_status,omitempty"`
	FromPriority   InboxCasePriority `gorm:"size:12" json:"from_priority,omitempty"`
	ToPriority     InboxCasePriority `gorm:"size:12" json:"to_priority,omitempty"`
}
