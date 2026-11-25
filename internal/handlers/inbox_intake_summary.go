package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type inboxIntakeFieldResult struct {
	DefinitionID uuid.UUID `json:"definition_id"`
	Label        string    `json:"label"`
	Scope        string    `json:"scope"`
	Status       string    `json:"status"`
}
type inboxIntakeHandoffSummary struct {
	ID                  uuid.UUID                `json:"id"`
	State               string                   `json:"state"`
	IssuedAt            time.Time                `json:"issued_at"`
	Outcomes            []inboxIntakeFieldResult `json:"outcomes"`
	ContactFieldsHidden bool                     `json:"contact_fields_hidden"`
}

func (a *App) GetInboxIntakeSummary(r *fastglue.Request) error {
	thread, user, err := a.inboxRequest(r, models.ActionRead)
	if err != nil {
		return nil
	}
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	claims, _ := r.RequestCtx.UserValue(middleware.ContextKeyClaims).(*middleware.JWTClaims)
	viewer, err := loadAuditViewer(db, thread.org, user, claims)
	if err != nil {
		if !errors.Is(err, errAuditUnauthenticated) && !errors.Is(err, errAuditForbidden) {
			return a.intakeError(r, err)
		}
		return r.SendErrorEnvelope(403, "You do not have access to this intake summary", nil, "")
	}
	if !viewer.has(models.ResourceChat, models.ActionRead) {
		return r.SendErrorEnvelope(403, "You do not have access to this intake summary", nil, "")
	}
	q := scopeInboxCaseHistoryWithContactRead(thread.query(db.Model(&models.InboxCase{})), user, thread.org, viewer.has(models.ResourceContacts, models.ActionRead))
	if raw := string(r.RequestCtx.QueryArgs().Peek("case_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			return a.inboxError(r, errInboxTransition)
		}
		q = q.Where("inbox_cases.id=?", id)
	}
	var c models.InboxCase
	if err := q.Order("inbox_cases.opened_at DESC,inbox_cases.id DESC").First(&c).Error; err != nil {
		return a.inboxError(r, err)
	}
	var bindings []models.InboxIntakeBinding
	if err := db.Where("organization_id=? AND account_id=? AND contact_id=? AND case_id=?", thread.org, thread.account, thread.contact, c.ID).Order("issued_at DESC,id DESC").Limit(51).Find(&bindings).Error; err != nil {
		return a.intakeError(r, err)
	}
	more := len(bindings) > 50
	if more {
		bindings = bindings[:50]
	}
	ids := []uuid.UUID{}
	for _, binding := range bindings {
		for _, outcome := range binding.Outcomes {
			ids = append(ids, outcome.DefinitionID)
		}
	}
	var definitions []models.InboxFieldDefinition
	if len(ids) > 0 {
		if err := db.Where("organization_id=? AND id IN ?", thread.org, ids).Find(&definitions).Error; err != nil {
			return a.intakeError(r, err)
		}
	}
	byID := map[uuid.UUID]models.InboxFieldDefinition{}
	for _, d := range definitions {
		byID[d.ID] = d
	}
	result := make([]inboxIntakeHandoffSummary, 0, len(bindings))
	for _, binding := range bindings {
		item := inboxIntakeHandoffSummary{ID: binding.ID, State: binding.State, IssuedAt: binding.IssuedAt, Outcomes: []inboxIntakeFieldResult{}}
		for _, outcome := range binding.Outcomes {
			d, exists := byID[outcome.DefinitionID]
			if !exists {
				continue
			}
			if d.Scope == "contact" && !viewer.has(models.ResourceContacts, models.ActionRead) {
				item.ContactFieldsHidden = true
				continue
			}
			item.Outcomes = append(item.Outcomes, inboxIntakeFieldResult{DefinitionID: d.ID, Label: d.Label, Scope: d.Scope, Status: outcome.Status})
		}
		result = append(result, item)
	}
	return r.SendEnvelope(map[string]any{"case_id": c.ID, "summaries": result, "has_more": more})
}

// Reuse the existing inbox maintenance worker. Expired staging is erased, not
// replayed on restore or on a future, unrelated human case.
func expireInboxIntake(db *gorm.DB, now time.Time) error {
	return privateInboxIntegrationDB(db).Model(&models.InboxIntakeBinding{}).Where("expires_at<=? AND state IN ?", now, []string{"pending", "staged"}).Updates(map[string]any{"state": "expired", "encrypted_answers": ""}).Error
}
