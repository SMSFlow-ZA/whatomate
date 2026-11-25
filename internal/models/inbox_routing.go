package models

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const InboxRoutingInherit AssignmentStrategy = "inherit"

// InboxRoutingPolicy is an unapplied template. Applied settings are copied, so
// editing or archiving this row never silently changes a running inbox.
type InboxRoutingPolicy struct {
	BaseModel
	OrganizationID uuid.UUID          `gorm:"type:uuid;not null;index" json:"-"`
	Revision       int64              `gorm:"not null;default:1" json:"revision"`
	Name           string             `gorm:"size:100;not null" json:"name"`
	Description    string             `gorm:"size:1000;not null;default:''" json:"description"`
	Strategy       AssignmentStrategy `gorm:"size:50;not null" json:"strategy"`
	MaxActive      *int               `gorm:"check:inbox_routing_active_bound,max_active IS NULL OR (max_active >= 0 AND max_active <= 10000)" json:"max_active"`
	MaxAssignments int                `gorm:"not null;default:0;check:inbox_routing_rate_bound,max_assignments >= 0 AND max_assignments <= 10000" json:"max_assignments"`
	WindowSeconds  int                `gorm:"not null;default:0;check:inbox_routing_window_bound,(max_assignments = 0 AND window_seconds = 0) OR (max_assignments > 0 AND window_seconds >= 60 AND window_seconds <= 86400)" json:"window_seconds"`
	ArchivedAt     *time.Time         `json:"archived_at,omitempty"`
	CreatedByID    uuid.UUID          `gorm:"type:uuid;not null" json:"-"`
	UpdatedByID    uuid.UUID          `gorm:"type:uuid;not null" json:"-"`
}

func ValidInboxRoutingStrategy(strategy AssignmentStrategy) bool {
	return strategy == InboxRoutingInherit || strategy == AssignmentStrategyManual || strategy == AssignmentStrategyRoundRobin || strategy == AssignmentStrategyLoadBalanced
}

func ValidInboxAssignmentRate(maximum, seconds int) bool {
	return maximum == 0 && seconds == 0 || maximum >= 1 && maximum <= 10000 && seconds >= 60 && seconds <= 86400
}

func (p InboxRoutingPolicy) Validate() error {
	if strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 100 || utf8.RuneCountInString(p.Description) > 1000 || !ValidInboxRoutingStrategy(p.Strategy) || !ValidInboxAssignmentRate(p.MaxAssignments, p.WindowSeconds) || p.MaxActive != nil && (*p.MaxActive < 0 || *p.MaxActive > 10000) {
		return errors.New("invalid routing policy")
	}
	return nil
}

// Strategy is scoped to one team/account. Agent limits are deliberately scoped
// to the account across teams, just like the existing active-case capacity.
type InboxRoutingConfiguration struct {
	OrganizationID uuid.UUID          `gorm:"type:uuid;primaryKey" json:"-"`
	AccountID      uuid.UUID          `gorm:"type:uuid;primaryKey" json:"account_id"`
	TeamID         uuid.UUID          `gorm:"type:uuid;primaryKey" json:"team_id"`
	Revision       int64              `gorm:"not null;default:1" json:"revision"`
	PolicyID       uuid.UUID          `gorm:"type:uuid;not null" json:"policy_id"`
	PolicyRevision int64              `gorm:"not null" json:"policy_revision"`
	Name           string             `gorm:"size:100;not null" json:"name"`
	Strategy       AssignmentStrategy `gorm:"size:50;not null" json:"strategy"`
	AppliedAt      time.Time          `gorm:"not null" json:"applied_at"`
	AppliedByID    uuid.UUID          `gorm:"type:uuid;not null" json:"-"`
	ApplyReceipt   string             `gorm:"size:64;not null" json:"-"`
}

type InboxAgentAssignmentRate struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	AccountID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	AgentID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"agent_id"`
	Revision       int64     `gorm:"not null;default:1" json:"revision"`
	MaxAssignments int       `gorm:"not null;check:inbox_agent_assignment_max,max_assignments >= 0 AND max_assignments <= 10000" json:"max_assignments"`
	WindowSeconds  int       `gorm:"not null;check:inbox_agent_assignment_window,(max_assignments = 0 AND window_seconds = 0) OR (max_assignments > 0 AND window_seconds >= 60 AND window_seconds <= 86400)" json:"window_seconds"`
}

// Receipts describe ownership changes, not attempts to assign. The database
// trigger records them in the same transaction, including legacy SQL writers.
// No contact identity, message content or secret belongs in this ledger.
type InboxAssignmentReceipt struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement" json:"-"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index:idx_inbox_assignment_window,priority:1" json:"-"`
	AccountID      uuid.UUID `gorm:"type:uuid;not null;index:idx_inbox_assignment_window,priority:2" json:"-"`
	AgentID        uuid.UUID `gorm:"type:uuid;not null;index:idx_inbox_assignment_window,priority:3" json:"-"`
	AssignedAt     time.Time `gorm:"not null;index:idx_inbox_assignment_window,priority:4" json:"-"`
	CaseID         uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
}
