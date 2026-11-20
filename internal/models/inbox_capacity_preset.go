package models

import "github.com/google/uuid"

// Applying a preset copies a value; later preset edits do not change limits.
type InboxCapacityPreset struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index" json:"-"`
	Name           string    `gorm:"size:100;not null" json:"name"`
	MaxActive      int       `gorm:"not null" json:"max_active"`
}
