package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type inboxSLANoticeItem struct {
	ID              uuid.UUID  `json:"id"`
	EventID         uuid.UUID  `json:"event_id"`
	CaseID          uuid.UUID  `json:"case_id"`
	AccountID       uuid.UUID  `json:"account_id"`
	ContactID       uuid.UUID  `json:"contact_id"`
	AccountName     string     `json:"account_name"`
	ContactName     string     `json:"contact_name"`
	PolicyName      string     `json:"policy_name"`
	PolicyVersionID uuid.UUID  `json:"policy_version_id"`
	Threshold       string     `json:"threshold"`
	Kind            string     `json:"kind"`
	DueAt           time.Time  `json:"due_at"`
	ObservedAt      time.Time  `json:"observed_at"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
	TargetState     string     `json:"target_state"`
}

const inboxSLANoticeColumns = "inbox_sla_notices.id,inbox_sla_notices.event_id,inbox_sla_notices.observed_at,inbox_sla_notices.read_at,e.case_id,e.account_id,ic.contact_id,wa.name AS account_name,co.profile_name AS contact_name,COALESCE(ver.selection->>'name','') AS policy_name,e.policy_version_id,e.threshold,e.kind,e.due_at,CASE WHEN ic.resolved_at IS NOT NULL OR ic.status='resolved' THEN 'case_resolved' WHEN o.completed_at IS NOT NULL THEN 'completed' WHEN o.paused THEN 'paused' WHEN o.deadline<CURRENT_TIMESTAMP THEN 'breached' ELSE 'active' END AS target_state"

// Both the receipt and all retained references must agree with current scope.
// A historic receipt is not an assignment or an organisation access grant.
func scopedInboxSLANotices(db *gorm.DB, org uuid.UUID, person inboxEscalationPerson) *gorm.DB {
	cases := scopeInboxCaseHistoryWithContactRead(db.Session(&gorm.Session{NewDB: true}).Model(&models.InboxCase{}), person.ID, org, person.FullContactRead).Select("inbox_cases.id")
	return db.Model(&models.InboxSLANotice{}).
		Joins("JOIN inbox_sla_escalation_events e ON e.id=inbox_sla_notices.event_id AND e.organization_id=inbox_sla_notices.organization_id AND e.deleted_at IS NULL").
		Joins("JOIN inbox_sla_obligations o ON o.id=e.obligation_id AND o.organization_id=e.organization_id AND o.account_id=e.account_id AND o.case_id=e.case_id AND o.policy_id=e.policy_version_id AND o.deleted_at IS NULL").
		Joins("JOIN inbox_sla_versions ver ON ver.id=e.policy_version_id AND ver.organization_id=e.organization_id AND ver.account_id=e.account_id AND ver.deleted_at IS NULL").
		Joins("JOIN inbox_cases ic ON ic.id=e.case_id AND ic.organization_id=e.organization_id AND ic.account_id=e.account_id AND ic.sla_policy_id=e.policy_version_id AND ic.deleted_at IS NULL").
		Joins("JOIN contacts co ON co.id=ic.contact_id AND co.organization_id=ic.organization_id AND co.deleted_at IS NULL").
		Joins("JOIN whatsapp_accounts wa ON wa.id=ic.account_id AND wa.organization_id=ic.organization_id AND wa.deleted_at IS NULL").
		Joins("JOIN organizations org ON org.id=e.organization_id AND org.inbox_workflows_enabled AND org.deleted_at IS NULL").
		Where("inbox_sla_notices.organization_id=? AND inbox_sla_notices.recipient_id=? AND inbox_sla_notices.outcome=? AND e.case_id IN (?)", org, person.ID, inboxNoticeAvailable, cases)
}

func inboxSLANoticePage(r *fastglue.Request, q *gorm.DB, prefix string) (*gorm.DB, error) {
	if offset := string(r.RequestCtx.QueryArgs().Peek("offset")); offset != "" && offset != "0" {
		return nil, errInboxTransition
	}
	if raw := string(r.RequestCtx.QueryArgs().Peek("cursor")); raw != "" {
		if len(raw) > 256 {
			return nil, errInboxTransition
		}
		data, err := base64.RawURLEncoding.DecodeString(raw)
		var cursor inboxIntegrationHistoryCursor
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.CreatedAt.IsZero() || cursor.ID == uuid.Nil {
			return nil, errInboxTransition
		}
		q = q.Where("("+prefix+".observed_at,"+prefix+".id)<(?,?)", cursor.CreatedAt, cursor.ID)
	}
	return q.Order(prefix + ".observed_at DESC," + prefix + ".id DESC").Limit(51), nil
}

func (a *App) ListInboxSLANotices(r *fastglue.Request) error {
	org, actor, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	people, err := a.inboxEscalationPeople(a.DB, org, []uuid.UUID{actor})
	if err != nil {
		return a.inboxError(r, err)
	}
	person, ok := people[actor]
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Internal notifications require current workspace chat access", nil, "")
	}
	q := scopedInboxSLANotices(a.DB, org, person)
	if raw := string(r.RequestCtx.QueryArgs().Peek("account_id")); raw != "" {
		account, err := uuid.Parse(raw)
		if err != nil || account == uuid.Nil {
			return a.inboxError(r, errInboxTransition)
		}
		q = q.Where("e.account_id=?", account)
	}
	switch string(r.RequestCtx.QueryArgs().Peek("unread")) {
	case "", "false":
	case "true":
		q = q.Where("inbox_sla_notices.read_at IS NULL")
	default:
		return a.inboxError(r, errInboxTransition)
	}
	q, err = inboxSLANoticePage(r, q, "inbox_sla_notices")
	if err != nil {
		return a.inboxError(r, err)
	}
	items := []inboxSLANoticeItem{}
	if err := q.Select(inboxSLANoticeColumns).Scan(&items).Error; err != nil {
		return a.inboxError(r, err)
	}
	mask, err := inboxMessageMasking(a.DB, org)
	if err != nil {
		return a.inboxError(r, err)
	}
	more, next := len(items) > 50, ""
	if more {
		items = items[:50]
		next = integrationHistoryCursor(items[49].ObservedAt, items[49].ID)
	}
	if mask {
		for i := range items {
			items[i].ContactName = utils.MaskIfPhoneNumber(items[i].ContactName)
		}
	}
	return r.SendEnvelope(map[string]any{"notices": items, "has_more": more, "next_cursor": next, "delivery": "in_app"})
}

func (a *App) ReadInboxSLANotice(r *fastglue.Request) error {
	org, actor, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "Internal notification")
	if err != nil {
		return nil
	}
	people, err := a.inboxEscalationPeople(a.DB, org, []uuid.UUID{actor})
	if err != nil {
		return a.inboxError(r, err)
	}
	person, ok := people[actor]
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Internal notifications require current workspace chat access", nil, "")
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		visible := scopedInboxSLANotices(tx, org, person).Where("inbox_sla_notices.id=?", id).Select("inbox_sla_notices.id")
		var count int64
		if err := visible.Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&models.InboxSLANotice{}).Where("organization_id=? AND recipient_id=? AND id=? AND read_at IS NULL AND id IN (?)", org, actor, id, visible).Update("read_at", time.Now().UTC()).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"id": id, "read": true})
}

func (a *App) canReceiveInboxNotice(org, actor, id uuid.UUID) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db := a.DB.WithContext(ctx)
	people, err := a.inboxEscalationPeople(db, org, []uuid.UUID{actor})
	if err != nil {
		return false
	}
	person, ok := people[actor]
	if !ok {
		return false
	}
	var notice struct {
		AccountID uuid.UUID
		Threshold string
	}
	q := scopedInboxSLANotices(db, org, person).
		Where("inbox_sla_notices.id=? AND inbox_sla_notices.read_at IS NULL AND o.completed_at IS NULL AND NOT o.paused AND ic.resolved_at IS NULL AND ic.status<>'resolved'", id).
		Where("e.due_at<=?", time.Now().UTC()).
		Where("e.threshold<>? OR o.deadline>=?", inboxSLAAtRisk, time.Now().UTC())
	if err := q.Select("e.account_id,e.threshold").Take(&notice).Error; err != nil {
		return false
	}
	settings, err := loadInboxEscalationSettings(db, org, notice.AccountID, false)
	return err == nil && inboxEscalationRecipientConfigured(settings, actor) &&
		((notice.Threshold == inboxSLAAtRisk && settings.AtRisk) || (notice.Threshold == inboxSLABreach && settings.Breach))
}
