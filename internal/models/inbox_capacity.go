package models

import "github.com/google/uuid"

// InboxAgentCapacity limits active cases for one agent on one WhatsApp account.
type InboxAgentCapacity struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	AccountID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	AgentID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"agent_id"`
	MaxActive      int       `gorm:"not null;check:inbox_agent_max_active_nonnegative,max_active >= 0" json:"max_active"`
}
