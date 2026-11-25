package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"time"
)

// AuditLogResponse retains legacy fields and explicitly labels captured context.
type AuditLogResponse struct {
	ID                uuid.UUID          `json:"id"`
	ResourceType      string             `json:"resource_type"`
	ResourceID        uuid.UUID          `json:"resource_id"`
	UserID            uuid.UUID          `json:"user_id"`
	UserName          string             `json:"user_name"`
	Action            models.AuditAction `json:"action"`
	Changes           models.JSONBArray  `json:"changes"`
	CreatedAt         time.Time          `json:"created_at"`
	ContextAvailable  bool               `json:"context_available"`
	ContextState      string             `json:"context_state"`
	ActorMasked       bool               `json:"actor_masked"`
	ActorAvailable    bool               `json:"actor_available"`
	OriginMasked      bool               `json:"origin_masked"`
	ResourceAvailable bool               `json:"resource_available"`
	models.AuditContext
}

// ListAuditLogs scopes and filters before counting or paging.
func (a *App) ListAuditLogs(r *fastglue.Request) error { return a.listScopedAuditLogs(r) }

// GetAuditLog uses the same fresh authority and scope as investigation/export.
func (a *App) GetAuditLog(r *fastglue.Request) error { return a.getScopedAuditLog(r) }
