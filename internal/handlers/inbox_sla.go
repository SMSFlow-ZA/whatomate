package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm/clause"
)

type inboxSLATarget struct {
	Status           string     `json:"status"`
	Deadline         *time.Time `json:"deadline,omitempty"`
	RemainingSeconds int64      `json:"remaining_seconds"`
}

type inboxSLASnapshot struct {
	FirstReply *inboxSLATarget `json:"first_reply,omitempty"`
	NextReply  *inboxSLATarget `json:"next_reply,omitempty"`
	Resolution *inboxSLATarget `json:"resolution,omitempty"`
}

type inboxSLACase struct {
	models.InboxCase
	SLA *inboxSLASnapshot `json:"sla,omitempty"`
}

func inboxPolicyClock(p models.InboxSLAPolicy) (inboxsla.Clock, error) {
	if p.BusinessHours {
		return inboxsla.BusinessClock(p.Timezone, p.Shifts)
	}
	return inboxsla.ElapsedClock(), nil
}

func measureInboxTarget(clock inboxsla.Clock, start time.Time, stop *time.Time, now time.Time, minutes int, pauses []inboxsla.Pause) *inboxSLATarget {
	if minutes <= 0 || start.IsZero() {
		return nil
	}
	budget := time.Duration(minutes) * time.Minute
	end := now
	if stop != nil {
		end = *stop
	}
	remaining := budget - clock.Elapsed(start, end, pauses)
	out := inboxSLATarget{RemainingSeconds: int64(remaining / time.Second)}
	switch {
	case stop != nil && remaining >= 0:
		out.Status = "met"
	case stop != nil || remaining <= 0:
		out.Status = "breached"
	case len(pauses) > 0 && pauses[len(pauses)-1].End.IsZero():
		out.Status = "paused"
	case remaining <= max(budget/5, 5*time.Minute):
		out.Status = "at_risk"
	default:
		out.Status = "on_track"
	}
	if stop == nil && out.Status != "paused" && out.Status != "breached" {
		deadline := clock.Deadline(end, remaining)
		out.Deadline = &deadline
	}
	return &out
}

func inboxSnapshot(clock inboxsla.Clock, policy models.InboxSLAPolicy, c models.InboxCase, transitions []models.InboxCaseActivity, now time.Time) *inboxSLASnapshot {
	var pauses []inboxsla.Pause
	for _, event := range transitions {
		if event.FromStatus == event.ToStatus || event.ToStatus == "" {
			continue
		}
		at := event.CreatedAt
		if event.OccurredAt != nil {
			at = *event.OccurredAt
		}
		if at.Before(c.OpenedAt) {
			continue
		}
		if event.ToStatus == models.InboxCaseWaiting {
			pauses = append(pauses, inboxsla.Pause{Start: at})
		} else if event.FromStatus == models.InboxCaseWaiting && len(pauses) > 0 {
			pauses[len(pauses)-1].End = at
		}
	}
	first := measureInboxTarget(clock, c.OpenedAt, c.FirstHumanReplyAt, now, policy.FirstReplyMinutes, nil)
	if c.ResolvedAt != nil && c.FirstHumanReplyAt == nil {
		first = measureInboxTarget(clock, c.OpenedAt, nil, *c.ResolvedAt, policy.FirstReplyMinutes, nil)
		if first != nil {
			first.Status = "unanswered"
			first.Deadline = nil
		}
	}
	var next *inboxSLATarget
	if c.NextReplyFrom != nil && c.ResolvedAt == nil {
		next = measureInboxTarget(clock, *c.NextReplyFrom, nil, now, policy.NextReplyMinutes, nil)
	}
	return &inboxSLASnapshot{FirstReply: first, NextReply: next, Resolution: measureInboxTarget(clock, c.OpenedAt, c.ResolvedAt, now, policy.ResolutionMinutes, pauses)}
}

func (a *App) inboxSLASnapshots(org uuid.UUID, cases []models.InboxCase, now time.Time) (map[uuid.UUID]*inboxSLASnapshot, error) {
	results := make(map[uuid.UUID]*inboxSLASnapshot)
	if len(cases) == 0 {
		return results, nil
	}
	accountIDs := make([]uuid.UUID, 0, len(cases))
	for _, c := range cases {
		accountIDs = append(accountIDs, c.AccountID)
	}
	var policies []models.InboxSLAPolicy
	if err := a.DB.Where("organization_id = ? AND account_id IN ?", org, accountIDs).Find(&policies).Error; err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return results, nil
	}
	type configured struct {
		policy models.InboxSLAPolicy
		clock  inboxsla.Clock
	}
	byAccount := make(map[uuid.UUID]configured)
	for _, p := range policies {
		clock, err := inboxPolicyClock(p)
		if err != nil {
			return nil, err
		}
		byAccount[p.AccountID] = configured{p, clock}
	}
	var caseIDs []uuid.UUID
	for _, c := range cases {
		if _, ok := byAccount[c.AccountID]; ok {
			caseIDs = append(caseIDs, c.ID)
		}
	}
	var events []models.InboxCaseActivity
	if err := a.DB.Where("organization_id = ? AND case_id IN ? AND (from_status = ? OR to_status = ?)", org, caseIDs,
		models.InboxCaseWaiting, models.InboxCaseWaiting).
		Order("COALESCE(occurred_at, created_at), created_at, id").Find(&events).Error; err != nil {
		return nil, err
	}
	byCase := make(map[uuid.UUID][]models.InboxCaseActivity)
	for _, event := range events {
		byCase[event.CaseID] = append(byCase[event.CaseID], event)
	}
	for _, c := range cases {
		if entry, ok := byAccount[c.AccountID]; ok {
			results[c.ID] = inboxSnapshot(entry.clock, entry.policy, c, byCase[c.ID], now)
		}
	}
	return results, nil
}

func validateInboxPolicy(p models.InboxSLAPolicy) error {
	for _, value := range []int{p.FirstReplyMinutes, p.NextReplyMinutes, p.ResolutionMinutes} {
		if value < 1 || value > 43200 {
			return errors.New("SLA targets must be between 1 and 43200 minutes")
		}
	}
	_, err := inboxPolicyClock(p)
	return err
}

func (a *App) ListInboxSLAPolicies(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	policies := []models.InboxSLAPolicy{}
	if err := a.DB.Where("organization_id = ?", org).Find(&policies).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"policies": policies})
}

func (a *App) SetInboxSLAPolicy(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	account, err := parsePathUUID(r, "account_id", "WhatsApp account")
	if err != nil {
		return nil
	}
	if _, err := findByIDAndOrg[models.WhatsAppAccount](a.DB, r, account, org, "WhatsApp account"); err != nil {
		return nil
	}
	var input models.InboxSLAPolicy
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.OrganizationID, input.AccountID = org, account
	if err := validateInboxPolicy(input); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	if err := a.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&input).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"policy": input})
}

func (a *App) DeleteInboxSLAPolicy(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	account, err := parsePathUUID(r, "account_id", "WhatsApp account")
	if err != nil {
		return nil
	}
	if err := a.DB.Where("organization_id = ? AND account_id = ?", org, account).Delete(&models.InboxSLAPolicy{}).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"deleted": true})
}

func (a *App) GetInboxSLAReport(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceAnalyticsAgents, models.ActionRead)
	if err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, org, false)
	if err != nil || !enabled {
		return a.inboxError(r, errInboxDisabled)
	}
	q := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).
		Where("inbox_cases.status <> ? AND EXISTS (SELECT 1 FROM inbox_sla_policies p WHERE p.organization_id = inbox_cases.organization_id AND p.account_id = inbox_cases.account_id)", models.InboxCaseResolved)
	for _, filter := range []string{"account_id", "team_id"} {
		if raw := r.RequestCtx.QueryArgs().Peek(filter); len(raw) > 0 {
			id, err := uuid.Parse(string(raw))
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid inbox report filter", nil, "")
			}
			q = q.Where("inbox_cases."+filter+" = ?", id)
		}
	}
	rows := []inboxQueueRow{}
	if err := q.Select("inbox_cases.*, wa.name AS account_name, c.profile_name AS contact_name").
		Order("inbox_cases.opened_at, inbox_cases.id").Offset(parseInboxQueuePage(r.RequestCtx.QueryArgs())).Limit(51).Scan(&rows).Error; err != nil {
		return a.inboxError(r, err)
	}
	rows, more, err := a.prepareInboxRows(org, rows)
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"cases": rows, "has_more": more})
}
