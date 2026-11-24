package models

import "github.com/google/uuid"

// InboxResponsePolicy is a prospective, ordered candidate. Its targets and
// matching definition belong to its immutable version, not this mutable row.
type InboxResponsePolicy struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index:idx_inbox_response_order,priority:1,where:deleted_at IS NULL" json:"-"`
	AccountID      uuid.UUID `gorm:"type:uuid;not null;index:idx_inbox_response_order,priority:2,where:deleted_at IS NULL" json:"account_id"`
	VersionID      uuid.UUID `gorm:"type:uuid;not null" json:"version_id"`
	Position       int       `gorm:"not null;index:idx_inbox_response_order,priority:3,where:deleted_at IS NULL" json:"position"`
	Revision       int64     `gorm:"not null;default:1" json:"revision"`
	Enabled        bool      `gorm:"not null;default:false" json:"enabled"`
}

// Team is an additional match restriction, outside the flat all/any group.
// Mandatory organisation/account/case authorisation is always separate.
type InboxPolicyConditions struct {
	TeamID     *uuid.UUID       `json:"team_id,omitempty"`
	Match      string           `json:"match,omitempty"`
	Predicates []InboxPredicate `json:"predicates"`
}

type InboxResponseDefinition struct {
	PolicyID    uuid.UUID             `json:"policy_id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Conditions  InboxPolicyConditions `json:"conditions"`
}

// InboxPolicyDecision records selection order as it was at queue entry, without
// copying condition values into agent-visible case history.
type InboxPolicyDecision struct {
	Kind     string     `json:"kind"`
	PolicyID *uuid.UUID `json:"policy_id,omitempty"`
	Position *int       `json:"position,omitempty"`
}
