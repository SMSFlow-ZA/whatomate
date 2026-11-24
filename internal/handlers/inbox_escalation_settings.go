package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxEscalationSettingsInput struct {
	Revision     int64       `json:"revision"`
	Enabled      *bool       `json:"enabled"`
	AtRisk       *bool       `json:"at_risk"`
	Breach       *bool       `json:"breach"`
	RecipientIDs []uuid.UUID `json:"recipient_ids"`
}

func (a *App) inboxEscalationContext(r *fastglue.Request, action string) (uuid.UUID, uuid.UUID, uuid.UUID, inboxEscalationPerson, bool) {
	org, actor, account, ok := a.responsePolicyContext(r, action)
	if !ok {
		return org, actor, account, inboxEscalationPerson{}, false
	}
	// Preserve global administrator configuration rights, but do not turn that
	// right into eligibility to receive an unrelated organisation's notices.
	var identity models.User
	if err := a.DB.Select("id,is_super_admin").First(&identity, "id=? AND is_active", actor).Error; err != nil {
		_ = a.inboxError(r, err)
		return org, actor, account, inboxEscalationPerson{}, false
	}
	person := inboxEscalationPerson{ID: actor, IsSuperAdmin: identity.IsSuperAdmin, FullContactRead: identity.IsSuperAdmin, SettingsRead: identity.IsSuperAdmin, SettingsWrite: identity.IsSuperAdmin, UsersRead: identity.IsSuperAdmin}
	if !identity.IsSuperAdmin {
		people, err := a.inboxEscalationPeople(a.DB, org, []uuid.UUID{actor})
		if err != nil {
			_ = a.inboxError(r, err)
			return org, actor, account, person, false
		}
		person = people[actor]
	}
	if !person.FullContactRead || (action == models.ActionRead && !person.SettingsRead) || (action == models.ActionWrite && !person.SettingsWrite) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Current full inbox and general settings access is required", nil, "")
		return org, actor, account, person, false
	}
	return org, actor, account, person, true
}

func loadInboxEscalationSettings(db *gorm.DB, org, account uuid.UUID, lock bool) (models.InboxSLAEscalationSettings, error) {
	row := models.InboxSLAEscalationSettings{OrganizationID: org, AccountID: account, AtRisk: true, Breach: true, RecipientIDs: []uuid.UUID{}}
	if lock {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := db.First(&row, "organization_id=? AND account_id=?", org, account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, nil
	}
	return row, err
}

func (a *App) GetInboxEscalationSettings(r *fastglue.Request) error {
	org, _, account, _, ok := a.inboxEscalationContext(r, models.ActionRead)
	if !ok {
		return nil
	}
	row, err := loadInboxEscalationSettings(a.DB, org, account, false)
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"settings": row, "maximum_recipients": inboxEscalationRecipientLimit, "delivery": "in_app"})
}

func validateInboxEscalationInput(input inboxEscalationSettingsInput) error {
	if input.Enabled == nil || input.AtRisk == nil || input.Breach == nil || input.Revision < 0 || len(input.RecipientIDs) > inboxEscalationRecipientLimit {
		return errInboxTransition
	}
	seen := make(map[uuid.UUID]bool)
	for _, id := range input.RecipientIDs {
		if id == uuid.Nil || seen[id] {
			return errInboxTransition
		}
		seen[id] = true
	}
	if *input.Enabled && (len(seen) == 0 || (!*input.AtRisk && !*input.Breach)) {
		return errInboxTransition
	}
	return nil
}

func (a *App) saveInboxEscalationSettings(org, actor, account uuid.UUID, input inboxEscalationSettingsInput, now time.Time) (models.InboxSLAEscalationSettings, error) {
	var row models.InboxSLAEscalationSettings
	if err := validateInboxEscalationInput(input); err != nil {
		return row, err
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		var err error
		row, err = loadInboxEscalationSettings(tx, org, account, true)
		if err != nil {
			return err
		}
		if row.Revision != input.Revision {
			return errInboxConflict
		}
		// Disabling must still work after a recipient loses access. Enabling
		// requires every explicitly selected identity to be eligible now.
		if *input.Enabled {
			people, err := a.inboxEscalationPeople(tx, org, input.RecipientIDs)
			if err != nil {
				return err
			}
			if len(people) != len(input.RecipientIDs) {
				return errInboxTransition
			}
		}
		arm := func(requested bool, wasActive bool, since *time.Time) *time.Time {
			if !*input.Enabled || !requested {
				return nil
			}
			if wasActive && since != nil {
				return since
			}
			at := now.UTC()
			return &at
		}
		row.AtRiskSince = arm(*input.AtRisk, row.Enabled && row.AtRisk, row.AtRiskSince)
		row.BreachSince = arm(*input.Breach, row.Enabled && row.Breach, row.BreachSince)
		row.Enabled, row.AtRisk, row.Breach = *input.Enabled, *input.AtRisk, *input.Breach
		row.RecipientIDs = append([]uuid.UUID{}, input.RecipientIDs...)
		row.Revision++
		row.UpdatedBy, row.UpdatedAt = actor, now.UTC()
		return tx.Save(&row).Error
	})
	return row, err
}

func (a *App) SetInboxEscalationSettings(r *fastglue.Request) error {
	org, actor, account, _, ok := a.inboxEscalationContext(r, models.ActionWrite)
	if !ok {
		return nil
	}
	var input inboxEscalationSettingsInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	row, err := a.saveInboxEscalationSettings(org, actor, account, input, time.Now().UTC())
	if errors.Is(err, errInboxConflict) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Internal notification settings changed. Refresh before reviewing another change.", nil, "inbox_escalation_conflict")
	}
	if errors.Is(err, errInboxTransition) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose a threshold and up to 20 active internal recipients with chat access before enabling notifications.", nil, "invalid_inbox_escalation")
	}
	if err != nil {
		return a.inboxError(r, err)
	}
	a.logAudit(org, actor, "inbox_sla_escalation_settings", account, models.AuditActionUpdated, nil,
		map[string]any{"enabled": row.Enabled, "at_risk": row.AtRisk, "breach": row.Breach, "recipient_count": len(row.RecipientIDs), "revision": row.Revision})
	return r.SendEnvelope(map[string]any{"settings": row, "delivery": "in_app"})
}
