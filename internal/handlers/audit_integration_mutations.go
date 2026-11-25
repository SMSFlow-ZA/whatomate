package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errAuditedIntegrationChanged = errors.New("integration changed")

func sendIntegrationConflict(r *fastglue.Request, err error) (bool, error) {
	if errors.Is(err, errAuditedIntegrationChanged) {
		return true, r.SendErrorEnvelope(409, "Integration changed; reload before saving", nil, "integration_changed")
	}
	return false, nil
}

func (a *App) recordIntegrationSecurity(r *fastglue.Request, tx *gorm.DB, org, actor uuid.UUID, resource string, id uuid.UUID, kind audit.EventKind, reason audit.Reason) error {
	provenance := a.requestAuditContext(r)
	if resource == "account" {
		provenance.AccountID = &id
	}
	return audit.RecordSecurity(tx, audit.SecurityEvent{OrganizationID: org, ActorID: actor, ResourceType: resource, ResourceID: id, Kind: kind, Outcome: audit.Success, Reason: reason, Context: provenance})
}

// Only local SQL belongs in write. ID points to a caller-owned model field so a
// database-generated creation ID is available to capture after the insert.
// Deleted targets are verified and recorded before hard deletion, in the same
// transaction. A failed write/capture rolls back both; no request payload enters
// the event. Existing targets are locked/scoped so Save cannot resurrect them.
func (a *App) mutateAuditedIntegration(r *fastglue.Request, org, actor uuid.UUID, resource string, id *uuid.UUID, reason audit.Reason, write func(*gorm.DB) error, expected ...time.Time) error {
	if id == nil || write == nil || len(expected) > 1 {
		return audit.ErrInvalidEvent
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return privateInboxIntegrationDB(a.DB).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if reason != audit.Created {
			table := map[string]string{"account": "whatsapp_accounts", "webhook": "webhooks", "custom_action": "custom_actions", "sso_provider": "sso_providers", "inbox_integration": "inbox_integrations", "inbox_integration_settings": "organizations"}[resource]
			if table == "" || *id == uuid.Nil {
				return audit.ErrInvalidEvent
			}
			query := tx.Table(table).Select("id,updated_at").Where("id=? AND deleted_at IS NULL", *id)
			if resource == "inbox_integration_settings" {
				if *id != org {
					return audit.ErrInvalidEvent
				}
			} else {
				query = query.Where("organization_id=?", org)
			}
			var guard struct {
				ID        uuid.UUID
				UpdatedAt time.Time
			}
			if err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&guard).Error; err != nil {
				return err
			}
			if len(expected) == 1 && !guard.UpdatedAt.Equal(expected[0]) {
				return errAuditedIntegrationChanged
			}
		}
		if reason == audit.Deleted {
			if err := a.recordIntegrationSecurity(r, tx, org, actor, resource, *id, audit.IntegrationChanged, reason); err != nil {
				return err
			}
			return write(tx)
		}
		if err := write(tx); err != nil {
			return err
		}
		return a.recordIntegrationSecurity(r, tx, org, actor, resource, *id, audit.IntegrationChanged, reason)
	})
}
