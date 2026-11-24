package models

import (
	"github.com/google/uuid"
	"time"
)

type InboxIntegrationInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
}

type InboxIntegrationOutput struct {
	Name         string    `json:"name"`
	DefinitionID uuid.UUID `json:"definition_id"`
}

// InboxIntegration stores no agent supplied destination. EndpointKey resolves
// against operator configuration. Credentials are encrypted and write-only.
type InboxIntegration struct {
	BaseModel
	OrganizationID uuid.UUID                `gorm:"type:uuid;not null;index" json:"-"`
	Name           string                   `gorm:"size:100;not null" json:"name"`
	Description    string                   `gorm:"size:1000;not null;default:''" json:"description"`
	EndpointKey    string                   `gorm:"size:64;not null" json:"endpoint_key"`
	AuthMode       string                   `gorm:"size:16;not null" json:"auth_mode"`
	Credential     string                   `gorm:"type:text;not null" json:"-"`
	Enabled        bool                     `gorm:"not null;default:false" json:"enabled"`
	Inputs         []InboxIntegrationInput  `gorm:"serializer:json;type:jsonb;not null" json:"inputs"`
	Outputs        []InboxIntegrationOutput `gorm:"serializer:json;type:jsonb;not null" json:"outputs"`
	ReferenceKey   string                   `gorm:"size:64;not null;default:''" json:"reference_key"`
	CreatedByID    uuid.UUID                `gorm:"type:uuid;not null" json:"created_by_id"`
}

// InboxIntegrationExecution is durable external work. The immutable request is
// encrypted; a lost result is held for reconciliation, never blindly replayed.
type InboxIntegrationExecution struct {
	BaseModel
	OrganizationID       uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	RunID                uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_inbox_integration_run_action,priority:1" json:"run_id"`
	ActionIndex          int        `gorm:"not null;uniqueIndex:idx_inbox_integration_run_action,priority:2" json:"action_index"`
	IntegrationID        uuid.UUID  `gorm:"type:uuid;not null;index" json:"integration_id"`
	CaseID               uuid.UUID  `gorm:"type:uuid;not null;index" json:"case_id"`
	AccountID            uuid.UUID  `gorm:"type:uuid;not null" json:"account_id"`
	ContactID            uuid.UUID  `gorm:"type:uuid;not null" json:"contact_id"`
	ActorID              uuid.UUID  `gorm:"type:uuid;not null" json:"actor_id"`
	DefinitionDigest     string     `gorm:"size:64;not null" json:"-"`
	RequestDigest        string     `gorm:"size:64;not null" json:"-"`
	CaseDigest           string     `gorm:"size:64;not null" json:"-"`
	EncryptedRequest     string     `gorm:"type:text;not null" json:"-"`
	EncryptedResult      string     `gorm:"type:text;not null;default:''" json:"-"`
	State                string     `gorm:"size:24;not null;index:idx_inbox_integration_pending,priority:1" json:"state"`
	Outcome              string     `gorm:"size:64;not null;default:''" json:"outcome"`
	AttemptCount         int        `gorm:"not null;default:0" json:"attempt_count"`
	RetryUntil           *time.Time `json:"retry_until,omitempty"`
	RetryRequested       bool       `gorm:"not null;default:false" json:"-"`
	StartedAt            *time.Time `gorm:"index:idx_inbox_integration_pending,priority:2" json:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
	OutputsAppliedAt     *time.Time `json:"outputs_applied_at,omitempty"`
	ProviderStatus       int        `gorm:"not null;default:0" json:"provider_status,omitempty"`
	DurationMillis       int64      `gorm:"not null;default:0" json:"duration_ms,omitempty"`
	ReconciledByID       *uuid.UUID `gorm:"type:uuid" json:"reconciled_by_id,omitempty"`
	ReconciliationReason string     `gorm:"size:1000;not null;default:''" json:"reconciliation_reason,omitempty"`
}

// InboxIntegrationRecovery retains each explicit recovery decision. Operation
// IDs make repeated UI submissions idempotent without erasing prior outcomes.
type InboxIntegrationRecovery struct {
	BaseModel
	OrganizationID   uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	ExecutionID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"execution_id"`
	ActorID          uuid.UUID  `gorm:"type:uuid;not null" json:"actor_id"`
	Kind             string     `gorm:"size:32;not null" json:"kind"`
	FromState        string     `gorm:"size:24;not null" json:"from_state"`
	FromOutcome      string     `gorm:"size:64;not null" json:"from_outcome,omitempty"`
	AttemptCount     int        `gorm:"not null" json:"attempt_count"`
	PriorStartedAt   *time.Time `json:"prior_started_at,omitempty"`
	PriorCompletedAt *time.Time `json:"prior_completed_at,omitempty"`
	ProviderStatus   int        `gorm:"not null;default:0" json:"provider_status,omitempty"`
	DurationMillis   int64      `gorm:"not null;default:0" json:"duration_ms,omitempty"`
	Reason           string     `gorm:"size:1000;not null" json:"reason"`
	RequestDigest    string     `gorm:"size:64;not null" json:"-"`
}
