package models

import (
	"time"

	"github.com/google/uuid"
)

// Notification routing is mutable and independent of immutable case targets.
// Threshold clocks arm only on explicit activation; old crossings are not replayed.
type InboxSLAEscalationSettings struct {
	OrganizationID uuid.UUID   `gorm:"type:uuid;primaryKey" json:"-"`
	AccountID      uuid.UUID   `gorm:"type:uuid;primaryKey" json:"account_id"`
	Revision       int64       `gorm:"not null" json:"revision"`
	Enabled        bool        `gorm:"not null;default:false" json:"enabled"`
	AtRisk         bool        `gorm:"not null;default:false" json:"at_risk"`
	Breach         bool        `gorm:"not null;default:false" json:"breach"`
	RecipientIDs   []uuid.UUID `gorm:"serializer:json;type:jsonb;not null;default:'[]'" json:"recipient_ids"`
	AtRiskSince    *time.Time  `json:"at_risk_since,omitempty"`
	BreachSince    *time.Time  `json:"breach_since,omitempty"`
	UpdatedBy      uuid.UUID   `gorm:"type:uuid;not null" json:"-"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// One observation per obligation/version/threshold, independently of later
// recipient edits or worker restarts. Outcomes and recipient receipts are atomic.
type InboxSLAEscalationEvent struct {
	BaseModel
	OrganizationID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_sla_escalation_once,priority:1;index:idx_inbox_sla_escalation_history,priority:1" json:"-"`
	AccountID        uuid.UUID `gorm:"type:uuid;not null;index:idx_inbox_sla_escalation_history,priority:2" json:"account_id"`
	CaseID           uuid.UUID `gorm:"type:uuid;not null;index" json:"case_id"`
	ObligationID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_sla_escalation_once,priority:2" json:"obligation_id"`
	PolicyVersionID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_sla_escalation_once,priority:3" json:"policy_version_id"`
	Threshold        string    `gorm:"size:16;not null;uniqueIndex:idx_inbox_sla_escalation_once,priority:4" json:"threshold"`
	Kind             string    `gorm:"size:16;not null" json:"kind"`
	SettingsRevision int64     `gorm:"not null" json:"settings_revision"`
	DueAt            time.Time `gorm:"not null" json:"due_at"`
	ObservedAt       time.Time `gorm:"not null;index:idx_inbox_sla_escalation_history,priority:3" json:"observed_at"`
	AvailableCount   int       `gorm:"not null" json:"available_count"`
	SuppressedCount  int       `gorm:"not null" json:"suppressed_count"`
}

// Available means retained in the app, not that a socket or human received it.
// Suppressed outcomes retain no message content or recipient profile details.
type InboxSLANotice struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;index:idx_inbox_sla_notice_recipient,priority:1" json:"-"`
	EventID        uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_sla_notice_once,priority:1" json:"event_id"`
	RecipientID    uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_sla_notice_once,priority:2;index:idx_inbox_sla_notice_recipient,priority:2" json:"-"`
	Outcome        string     `gorm:"size:32;not null;index:idx_inbox_sla_notice_recipient,priority:3" json:"outcome"`
	ObservedAt     time.Time  `gorm:"not null;index:idx_inbox_sla_notice_recipient,priority:4" json:"observed_at"`
	ReadAt         *time.Time `json:"read_at,omitempty"`
}
