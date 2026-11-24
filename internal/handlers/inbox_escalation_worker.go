package handlers

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const inboxSLAAtRisk = "at_risk"
const inboxSLABreach = "breach"
const inboxNoticeAvailable = "available"

type inboxEscalationCandidate struct {
	ID             uuid.UUID
	CaseID         uuid.UUID
	OrganizationID uuid.UUID
}

// Columns and operators are fixed here, never supplied by a request.
func inboxEscalationDueQuery(db *gorm.DB, threshold string, now time.Time) *gorm.DB {
	q := db.Model(&models.InboxSLAObligation{}).
		Joins("JOIN inbox_cases ic ON ic.id=inbox_sla_obligations.case_id AND ic.organization_id=inbox_sla_obligations.organization_id AND ic.account_id=inbox_sla_obligations.account_id AND ic.sla_policy_id=inbox_sla_obligations.policy_id AND ic.deleted_at IS NULL AND ic.resolved_at IS NULL AND ic.status<>'resolved'").
		Joins("JOIN contacts co ON co.id=ic.contact_id AND co.organization_id=ic.organization_id AND co.deleted_at IS NULL").
		Joins("JOIN whatsapp_accounts wa ON wa.id=ic.account_id AND wa.organization_id=ic.organization_id AND wa.deleted_at IS NULL").
		Joins("JOIN organizations org ON org.id=ic.organization_id AND org.inbox_workflows_enabled AND org.deleted_at IS NULL").
		Joins("JOIN inbox_sla_escalation_settings es ON es.organization_id=ic.organization_id AND es.account_id=ic.account_id AND es.enabled").
		Where("inbox_sla_obligations.completed_at IS NULL AND NOT inbox_sla_obligations.paused AND inbox_sla_obligations.deadline IS NOT NULL").
		Where("NOT EXISTS (SELECT 1 FROM inbox_sla_escalation_events ev WHERE ev.organization_id=inbox_sla_obligations.organization_id AND ev.obligation_id=inbox_sla_obligations.id AND ev.policy_version_id=inbox_sla_obligations.policy_id AND ev.threshold=?)", threshold)
	if threshold == inboxSLAAtRisk {
		return q.Where("es.at_risk AND es.at_risk_since IS NOT NULL AND inbox_sla_obligations.at_risk_at>=es.at_risk_since AND inbox_sla_obligations.at_risk_at<=? AND inbox_sla_obligations.deadline>=?", now, now).
			Order("inbox_sla_obligations.at_risk_at,inbox_sla_obligations.id")
	}
	return q.Where("es.breach AND es.breach_since IS NOT NULL AND inbox_sla_obligations.deadline>=es.breach_since AND inbox_sla_obligations.deadline<?", now).
		Order("inbox_sla_obligations.deadline,inbox_sla_obligations.id")
}

func inboxEscalationDue(settings models.InboxSLAEscalationSettings, obligation models.InboxSLAObligation, threshold string, now time.Time) *time.Time {
	if !settings.Enabled || obligation.CompletedAt != nil || obligation.Paused || obligation.Deadline == nil || len(settings.RecipientIDs) == 0 || len(settings.RecipientIDs) > inboxEscalationRecipientLimit {
		return nil
	}
	if threshold == inboxSLAAtRisk {
		if settings.AtRisk && settings.AtRiskSince != nil && obligation.AtRiskAt != nil && !obligation.AtRiskAt.Before(*settings.AtRiskSince) && !now.Before(*obligation.AtRiskAt) && !now.After(*obligation.Deadline) {
			return obligation.AtRiskAt
		}
	} else if threshold == inboxSLABreach && settings.Breach && settings.BreachSince != nil && !obligation.Deadline.Before(*settings.BreachSince) && now.After(*obligation.Deadline) {
		return obligation.Deadline
	}
	return nil
}

// A recorded handoff links the transfer to this case. Merely sharing a contact
// cannot suppress an unrelated case/account. Legacy escalation covers replies,
// not the separate inbox resolution target.
func inboxLegacyEscalationCoverage(db *gorm.DB, c models.InboxCase, obligation models.InboxSLAObligation) (map[uuid.UUID]bool, error) {
	covered := make(map[uuid.UUID]bool)
	if obligation.Kind != "first_reply" && obligation.Kind != "next_reply" {
		return covered, nil
	}
	var settings models.ChatbotSettings
	err := db.Select("sla_enabled", "sla_escalation_minutes", "sla_escalation_notify_ids").First(&settings, "organization_id=?", c.OrganizationID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return covered, nil
	}
	if err != nil || !settings.SLA.Enabled || settings.SLA.EscalationMinutes <= 0 || len(settings.SLA.EscalationNotifyIDs) == 0 {
		return covered, err
	}
	var transfers int64
	err = db.Model(&models.AgentTransfer{}).
		Where("organization_id=? AND contact_id=? AND status=? AND sla_escalation_at IS NOT NULL", c.OrganizationID, c.ContactID, models.TransferStatusActive).
		Where("whats_app_account IN (SELECT name FROM whatsapp_accounts WHERE id=? AND organization_id=? AND deleted_at IS NULL)", c.AccountID, c.OrganizationID).
		Where("(escalation_level<2 OR escalated_at>=?)", obligation.StartedAt).
		Where("EXISTS (SELECT 1 FROM inbox_case_activities act WHERE act.organization_id=agent_transfers.organization_id AND act.account_id=? AND act.case_id=? AND act.event_key=('handoff:' || agent_transfers.id::text) AND act.deleted_at IS NULL)", c.AccountID, c.ID).
		Count(&transfers).Error
	if err != nil || transfers == 0 {
		return covered, err
	}
	for _, raw := range settings.SLA.EscalationNotifyIDs {
		if id, err := uuid.Parse(raw); err == nil {
			covered[id] = true
		}
	}
	return covered, nil
}

func (a *App) processInboxEscalation(ctx context.Context, candidate inboxEscalationCandidate, threshold string, now time.Time) error {
	var available []models.InboxSLANotice
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c models.InboxCase
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&c, "id=? AND organization_id=?", candidate.CaseID, candidate.OrganizationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if c.ResolvedAt != nil || c.Status == models.InboxCaseResolved || c.SLAPolicyID == nil {
			return nil
		}
		if c.SLAProjectionVersion != c.Version {
			if err := inboxquality.Sync(tx, &c, now); err != nil {
				return err
			}
		}
		var obligation models.InboxSLAObligation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&obligation, "id=? AND organization_id=? AND case_id=? AND account_id=? AND policy_id=?", candidate.ID, c.OrganizationID, c.ID, c.AccountID, *c.SLAPolicyID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var settings models.InboxSLAEscalationSettings
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&settings, "organization_id=? AND account_id=?", c.OrganizationID, c.AccountID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		due := inboxEscalationDue(settings, obligation, threshold, now)
		if due == nil {
			return nil
		}
		// Reuse the complete candidate scope after locks: an account/contact,
		// feature switch or projection can have changed since discovery.
		var eligible int64
		if err := inboxEscalationDueQuery(tx, threshold, now).Where("inbox_sla_obligations.id=?", obligation.ID).Count(&eligible).Error; err != nil || eligible == 0 {
			return err
		}
		var version models.InboxSLAVersion
		if err := tx.Select("id").First(&version, "id=? AND organization_id=? AND account_id=?", obligation.PolicyID, c.OrganizationID, c.AccountID).Error; err != nil {
			return err
		}
		people, err := a.inboxEscalationPeople(tx, c.OrganizationID, settings.RecipientIDs)
		if err != nil {
			return err
		}
		covered, err := inboxLegacyEscalationCoverage(tx, c, obligation)
		if err != nil {
			return err
		}
		event := models.InboxSLAEscalationEvent{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: c.OrganizationID, AccountID: c.AccountID, CaseID: c.ID, ObligationID: obligation.ID, PolicyVersionID: obligation.PolicyID, Threshold: threshold, Kind: obligation.Kind, SettingsRevision: settings.Revision, DueAt: *due, ObservedAt: now}
		var notices []models.InboxSLANotice
		for _, recipient := range settings.RecipientIDs {
			outcome := "recipient_unavailable"
			if person, ok := people[recipient]; ok {
				canRead, err := inboxEscalationPersonCanRead(tx, c.OrganizationID, person, c.ID)
				if err != nil {
					return err
				}
				switch {
				case !canRead:
					outcome = "case_access_denied"
				case covered[recipient]:
					outcome = "legacy_transfer_covered"
				default:
					outcome = inboxNoticeAvailable
				}
			}
			notice := models.InboxSLANotice{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: c.OrganizationID, EventID: event.ID, RecipientID: recipient, Outcome: outcome, ObservedAt: now}
			notices = append(notices, notice)
			if outcome == inboxNoticeAvailable {
				event.AvailableCount++
			} else {
				event.SuppressedCount++
			}
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		if err := tx.Create(&notices).Error; err != nil {
			return err
		}
		for _, notice := range notices {
			if notice.Outcome == inboxNoticeAvailable {
				available = append(available, notice)
			}
		}
		return nil
	})
	if err == nil && a.WSHub != nil {
		for _, notice := range available {
			a.WSHub.Broadcast(websocket.BroadcastMessage{OrgID: notice.OrganizationID, UserID: notice.RecipientID,
				Message: websocket.WSMessage{Type: websocket.TypeInboxNoticeAvailable, Payload: map[string]string{"id": notice.ID.String()}},
				Authorize: func(user uuid.UUID) bool {
					return user == notice.RecipientID && a.canReceiveInboxNotice(notice.OrganizationID, user, notice.ID)
				}})
		}
	}
	return err
}

func (a *App) runInboxEscalationCycle(ctx context.Context, now time.Time) error {
	var cycleError error
	for _, threshold := range []string{inboxSLABreach, inboxSLAAtRisk} {
		var candidates []inboxEscalationCandidate
		if err := inboxEscalationDueQuery(a.DB.WithContext(ctx), threshold, now).
			Select("inbox_sla_obligations.id,inbox_sla_obligations.case_id,inbox_sla_obligations.organization_id").Limit(10).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, candidate := range candidates {
			if err := a.processInboxEscalation(ctx, candidate, threshold, now); err != nil {
				cycleError = errors.Join(cycleError, err)
			}
		}
	}
	return cycleError
}

func (a *App) RunInboxEscalations(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		cycleContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := a.runInboxEscalationCycle(cycleContext, time.Now().UTC())
		cancel()
		if err != nil && ctx.Err() == nil {
			a.Log.Error("Internal inbox notification cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func inboxEscalationRecipientConfigured(settings models.InboxSLAEscalationSettings, recipient uuid.UUID) bool {
	return settings.Enabled && slices.Contains(settings.RecipientIDs, recipient)
}
