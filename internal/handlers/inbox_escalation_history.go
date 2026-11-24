package handlers

import (
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func (a *App) ListInboxEscalationRecipients(r *fastglue.Request) error {
	org, _, _, actor, ok := a.inboxEscalationContext(r, models.ActionRead)
	if !ok {
		return nil
	}
	if !actor.UsersRead {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Selecting staff requires user-directory read access", nil, "")
	}
	search := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("search")))
	if utf8.RuneCountInString(search) > 100 {
		return a.inboxError(r, errInboxTransition)
	}
	q := a.DB.Model(&models.User{}).
		Where("is_active AND (organization_id=? OR EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.user_id=users.id AND uo.organization_id=? AND uo.role_id IS NOT NULL AND uo.deleted_at IS NULL))", org, org)
	if search != "" {
		pattern := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(search)
		q = q.Where("full_name ILIKE ?", "%"+pattern+"%")
	}
	if raw := string(r.RequestCtx.QueryArgs().Peek("cursor")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			return a.inboxError(r, errInboxTransition)
		}
		q = q.Where("users.id>?", id)
	}
	var ids []uuid.UUID
	if err := q.Order("users.id").Limit(101).Pluck("users.id", &ids).Error; err != nil {
		return a.inboxError(r, err)
	}
	more, next := len(ids) > 100, ""
	if more {
		ids = ids[:100]
		next = ids[99].String()
	}
	people, err := a.inboxEscalationPeople(a.DB, org, ids)
	if err != nil {
		return a.inboxError(r, err)
	}
	type option struct {
		ID              uuid.UUID `json:"id"`
		Name            string    `json:"name"`
		FullContactRead bool      `json:"full_contact_read"`
	}
	items := []option{}
	for _, id := range ids {
		if person, ok := people[id]; ok {
			items = append(items, option{person.ID, person.FullName, person.FullContactRead})
		}
	}
	return r.SendEnvelope(map[string]any{"recipients": items, "has_more": more, "next_cursor": next})
}

func (a *App) ListInboxEscalationHistory(r *fastglue.Request) error {
	org, _, account, person, ok := a.inboxEscalationContext(r, models.ActionRead)
	if !ok {
		return nil
	}
	cases := scopeInboxCaseHistoryWithContactRead(a.DB.Model(&models.InboxCase{}), person.ID, org, person.FullContactRead).
		Where("inbox_cases.account_id=e.account_id AND inbox_cases.sla_policy_id=e.policy_version_id").Select("inbox_cases.id")
	q := a.DB.Model(&models.InboxSLAEscalationEvent{}).Table("inbox_sla_escalation_events e").
		Joins("JOIN inbox_sla_obligations o ON o.id=e.obligation_id AND o.organization_id=e.organization_id AND o.account_id=e.account_id AND o.case_id=e.case_id AND o.policy_id=e.policy_version_id AND o.deleted_at IS NULL").
		Joins("JOIN inbox_sla_versions ver ON ver.id=e.policy_version_id AND ver.organization_id=e.organization_id AND ver.account_id=e.account_id AND ver.deleted_at IS NULL").
		Where("e.organization_id=? AND e.account_id=? AND e.case_id IN (?)", org, account, cases)
	q, err := inboxSLANoticePage(r, q, "e")
	if err != nil {
		return a.inboxError(r, err)
	}
	var events []models.InboxSLAEscalationEvent
	if err := q.Select("e.*").Find(&events).Error; err != nil {
		return a.inboxError(r, err)
	}
	more, next := len(events) > 50, ""
	if more {
		events = events[:50]
		next = integrationHistoryCursor(events[49].ObservedAt, events[49].ID)
	}
	ids := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	type receipt struct {
		ID          uuid.UUID `json:"id"`
		EventID     uuid.UUID `json:"event_id"`
		RecipientID uuid.UUID `json:"recipient_id"`
		Outcome     string    `json:"outcome"`
	}
	var receipts []receipt
	if len(ids) > 0 {
		if err := a.DB.Model(&models.InboxSLANotice{}).Select("id,event_id,recipient_id,outcome").Where("organization_id=? AND event_id IN ?", org, ids).Order("event_id,recipient_id").Scan(&receipts).Error; err != nil {
			return a.inboxError(r, err)
		}
	}
	type historyItem struct {
		models.InboxSLAEscalationEvent
		Recipients []receipt `json:"recipients"`
	}
	items := make([]historyItem, 0, len(events))
	byEvent := make(map[uuid.UUID][]receipt, len(events))
	for _, receipt := range receipts {
		byEvent[receipt.EventID] = append(byEvent[receipt.EventID], receipt)
	}
	for _, event := range events {
		item := historyItem{InboxSLAEscalationEvent: event, Recipients: []receipt{}}
		if receipts := byEvent[event.ID]; receipts != nil {
			item.Recipients = receipts
		}
		items = append(items, item)
	}
	return r.SendEnvelope(map[string]any{"events": items, "has_more": more, "next_cursor": next, "delivery": "in_app"})
}
