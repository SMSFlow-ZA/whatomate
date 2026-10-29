package models

import (
	"github.com/google/uuid"
	"time"
)

// ChannelConnection contains only public configuration in its JSON form.
// Credentials and event checkpoints never leave the server.
type ChannelConnection struct {
	BaseModel
	OrganizationID      uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name                string     `gorm:"size:100;not null" json:"name"`
	Provider            string     `gorm:"size:50;not null" json:"provider"`
	Enabled             bool       `gorm:"not null" json:"enabled"`
	MaxSegments         int        `gorm:"not null;default:3" json:"max_segments"`
	Credentials         string     `gorm:"type:text;not null" json:"-"`
	Capabilities        JSONB      `gorm:"type:jsonb;not null" json:"capabilities"`
	Cursor              string     `gorm:"type:text" json:"-"`
	LastPollAt          *time.Time `json:"last_poll_at,omitempty"`
	NextPollAt          time.Time  `gorm:"index" json:"-"`
	PollFailures        int        `json:"poll_failures"`
	ErrorCode           string     `gorm:"size:64" json:"error_code,omitempty"`
	BotFlowID           *uuid.UUID `gorm:"type:uuid" json:"bot_flow_id,omitempty"`
	BotConsentConfirmed bool       `gorm:"not null;default:false" json:"bot_consent_confirmed"`
	ValidatedAt         *time.Time `json:"validated_at,omitempty"`
	Balance             JSONB      `gorm:"type:jsonb" json:"balance,omitempty"`
	BalanceCheckedAt    *time.Time `json:"balance_checked_at,omitempty"`
	LowBalance          bool       `gorm:"not null;default:false" json:"low_balance"`
	BalanceThreshold    string     `gorm:"size:24;not null;default:'100'" json:"balance_threshold"`
	BalanceErrorCode    string     `gorm:"size:64" json:"balance_error_code,omitempty"`
}

// ChannelBotJob commits with the inbound event and cursor. Processing commits
// session state and outbound queues together; dispatch happens only afterwards.
type ChannelBotJob struct {
	BaseModel
	OrganizationID    uuid.UUID `gorm:"type:uuid;index;not null"`
	ConnectionID      uuid.UUID `gorm:"type:uuid;index;not null"`
	IncomingMessageID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null"`
	FlowID            uuid.UUID `gorm:"type:uuid;not null"`
	Status            string    `gorm:"size:20;index;not null"`
	ErrorCode         string    `gorm:"size:64"`
}

type ChannelAttempt struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	MessageID      uuid.UUID  `gorm:"type:uuid;index;not null" json:"message_id"`
	ConnectionID   *uuid.UUID `gorm:"type:uuid;index" json:"connection_id,omitempty"`
	Channel        string     `gorm:"size:20;not null;uniqueIndex:idx_channel_message" json:"channel"`
	// One attempt of each channel per logical message; fallback never fans out.
	LogicalMessageID  uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_channel_message" json:"-"`
	Status            string     `gorm:"size:24;index;not null" json:"status"`
	Text              string     `gorm:"type:text" json:"text,omitempty"`
	Destination       string     `gorm:"size:20" json:"-"`
	ProviderMessageID string     `gorm:"size:255;index" json:"provider_message_id,omitempty"`
	IsFallback        bool       `gorm:"not null" json:"is_fallback"`
	DueAt             *time.Time `gorm:"index" json:"due_at,omitempty"`
	DispatchedAt      *time.Time `gorm:"index" json:"dispatched_at,omitempty"`
	NextReconcileAt   *time.Time `gorm:"index" json:"-"`
	ReconcileFailures int        `gorm:"not null;default:0" json:"-"`
	LeaseUntil        *time.Time `json:"-"`
	ClaimID           *uuid.UUID `gorm:"type:uuid" json:"-"`
	ErrorCode         string     `gorm:"size:64" json:"error_code,omitempty"`
	Source            string     `gorm:"size:20" json:"source"`
	InitiatedBy       *uuid.UUID `gorm:"type:uuid" json:"-"`
	ConsentConfirmed  bool       `gorm:"not null" json:"-"`
}

// ChannelReceipt's unique key makes replaying an event page safe. Unknown or
// unmappable events are quarantined here, without exposing them to a contact.
type ChannelReceipt struct {
	BaseModel
	ConnectionID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_channel_event"`
	EventID      string    `gorm:"size:128;not null;uniqueIndex:idx_channel_event"`
	Kind         string    `gorm:"size:20;not null"`
	Quarantined  bool      `gorm:"not null"`
}
