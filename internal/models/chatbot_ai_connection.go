package models

import (
	"github.com/google/uuid"
	"time"
)

// ChatbotAIConnection holds workspace-owned, write-only inference credentials.
type ChatbotAIConnection struct {
	BaseModel
	OrganizationID  uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	Name            string     `gorm:"size:100;not null" json:"name"`
	EndpointID      string     `gorm:"size:63;not null" json:"endpoint_id"`
	Model           string     `gorm:"size:150;not null" json:"model"`
	Credential      string     `gorm:"type:text;not null" json:"-"`
	Enabled         bool       `gorm:"not null" json:"enabled"`
	Revision        int        `gorm:"not null" json:"revision"`
	TestedPolicy    string     `gorm:"size:64" json:"-"`
	CredentialScope string     `gorm:"size:64" json:"-"`
	TestedAt        *time.Time `json:"tested_at"`
}

// ChatbotAIBinding contains only the AI connection part of chatbot settings.
// Its account override does not shadow inherited chatbot settings. A nil UUID
// selects the organization default.
type ChatbotAIBinding struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	AccountID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"account_id"`
	ConnectionID   uuid.UUID `gorm:"type:uuid;not null;index" json:"connection_id"`
	IncludeHistory bool      `gorm:"not null" json:"include_history"`
	IncludeContext bool      `gorm:"not null" json:"include_context"`
}
