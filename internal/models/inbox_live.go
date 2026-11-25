package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// InboxLiveResource is an invalidation revision, not case content or pending
// work. A per-resource row serializes committed changes without treating a
// sequence allocated before commit as a safe global commit cursor.
type InboxLiveResource struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	Kind           string    `gorm:"size:24;primaryKey;check:inbox_live_kind,kind IN ('case','contact','configuration','workload','access')" json:"-"`
	ResourceID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"-"`
	Version        int64     `gorm:"not null;default:1;check:inbox_live_version,version>0" json:"-"`
	UpdatedAt      time.Time `gorm:"not null" json:"-"`
}

// InboxLiveChange exists only inside its writer's transaction. The deferred
// flush publishes sorted, coalesced resource revisions and deletes these rows
// before commit. It is neither a job queue nor restoreable pending dispatch.
type InboxLiveChange struct {
	TransactionID  string          `gorm:"type:xid8;primaryKey" json:"-"`
	OrganizationID uuid.UUID       `gorm:"type:uuid;primaryKey" json:"-"`
	Kind           string          `gorm:"size:24;primaryKey" json:"-"`
	ResourceID     uuid.UUID       `gorm:"type:uuid;primaryKey" json:"-"`
	BeforeScope    json.RawMessage `gorm:"type:jsonb" json:"-"`
	AfterScope     json.RawMessage `gorm:"type:jsonb" json:"-"`
}
