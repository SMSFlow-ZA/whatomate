package models

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
)

type InboxSLAPolicy struct {
	OrganizationID    uuid.UUID        `gorm:"type:uuid;primaryKey" json:"-"`
	AccountID         uuid.UUID        `gorm:"type:uuid;primaryKey" json:"account_id"`
	FirstReplyMinutes int              `json:"first_reply_minutes"`
	NextReplyMinutes  int              `json:"next_reply_minutes"`
	ResolutionMinutes int              `json:"resolution_minutes"`
	Timezone          string           `json:"timezone"`
	BusinessHours     bool             `json:"business_hours"`
	Shifts            []inboxsla.Shift `gorm:"serializer:json;type:jsonb" json:"shifts"`
}
