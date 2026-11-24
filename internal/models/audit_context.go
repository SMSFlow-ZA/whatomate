package models

import "github.com/google/uuid"

// Zero-version rows retain their historical meaning: context was not captured.
// No IP, outcome or correlation is inferred when reading an older backup.
type AuditContext struct {
	ContextVersion int        `gorm:"not null;default:0" json:"context_version"`
	Category       string     `gorm:"size:20;not null;default:'configuration'" json:"category"`
	Event          string     `gorm:"size:64;not null;default:'';uniqueIndex:idx_audit_request_event,priority:3" json:"event,omitempty"`
	Outcome        string     `gorm:"size:20;not null;default:''" json:"outcome,omitempty"`
	Reason         string     `gorm:"size:64;not null;default:''" json:"reason,omitempty"`
	Permission     string     `gorm:"size:100;not null;default:''" json:"permission,omitempty"`
	RequestID      string     `gorm:"size:36;not null;default:'';uniqueIndex:idx_audit_request_event,priority:2" json:"request_id,omitempty"`
	OriginIP       string     `gorm:"size:45;not null;default:''" json:"origin_ip,omitempty"`
	OriginSource   string     `gorm:"size:20;not null;default:''" json:"origin_source,omitempty"`
	ActorVerified  bool       `gorm:"not null;default:false" json:"actor_verified"`
	AccountID      *uuid.UUID `gorm:"type:uuid" json:"account_id,omitempty"`
	CaseID         *uuid.UUID `gorm:"type:uuid" json:"case_id,omitempty"`
}
