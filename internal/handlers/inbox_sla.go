package handlers

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxSLATarget struct {
	Status           string     `json:"status"`
	Deadline         *time.Time `json:"deadline,omitempty"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	Timezone         string     `json:"timezone"`
}

type inboxSLASnapshot struct {
	PolicyVersion *uuid.UUID          `json:"policy_version,omitempty"`
	PolicyName    string              `json:"policy_name,omitempty"`
	Legacy        bool                `json:"legacy"`
	SnoozeOptions []inboxSnoozeOption `json:"snooze_options,omitempty"`
	FirstReply    *inboxSLATarget     `json:"first_reply,omitempty"`
	NextReply     *inboxSLATarget     `json:"next_reply,omitempty"`
	Resolution    *inboxSLATarget     `json:"resolution,omitempty"`
}

type inboxSLACase struct {
	models.InboxCase
	SLA *inboxSLASnapshot `json:"sla,omitempty"`
}

func inboxPolicyClock(p models.InboxSLAPolicy) (inboxsla.Clock, error) {
	return inboxquality.PolicyClock(p)
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
	out.Deadline = inboxquality.Due(clock, start, budget, pauses)
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
	out := &inboxSLASnapshot{PolicyVersion: c.SLAPolicyID, Legacy: c.SLALegacy, FirstReply: first, NextReply: next, Resolution: measureInboxTarget(clock, c.OpenedAt, c.ResolvedAt, now, policy.ResolutionMinutes, pauses)}
	for _, target := range []*inboxSLATarget{out.FirstReply, out.NextReply, out.Resolution} {
		if target != nil {
			target.Timezone = policy.Timezone
			if target.Timezone == "" {
				target.Timezone = "UTC"
			}
		}
	}
	return out
}

func (a *App) inboxSLASnapshots(org uuid.UUID, cases []models.InboxCase, now time.Time) (map[uuid.UUID]*inboxSLASnapshot, error) {
	results := make(map[uuid.UUID]*inboxSLASnapshot)
	if len(cases) == 0 {
		return results, nil
	}
	type configured struct {
		policy models.InboxSLAPolicy
		clock  inboxsla.Clock
		name   string
	}
	byCasePolicy := make(map[uuid.UUID]configured)
	var ids []uuid.UUID
	for _, c := range cases {
		if c.SLAPolicyID != nil {
			ids = append(ids, *c.SLAPolicyID)
		}
	}
	var versions []models.InboxSLAVersion
	if len(ids) > 0 {
		if err := a.DB.Where("organization_id = ? AND id IN ?", org, ids).Find(&versions).Error; err != nil {
			return nil, err
		}
	}
	versionsByID := make(map[uuid.UUID]models.InboxSLAVersion)
	for _, v := range versions {
		versionsByID[v.ID] = v
	}
	for _, c := range cases {
		var p models.InboxSLAPolicy
		var name string
		if c.SLAPolicyID != nil {
			v, ok := versionsByID[*c.SLAPolicyID]
			if !ok || v.AccountID != c.AccountID {
				return nil, errors.New("case response policy version is missing")
			}
			p = v.Policy
			if v.Selection != nil {
				name = v.Selection.Name
			}
		} else if !c.SLAPolicyAssigned {
			// Read-only compatibility for fixture/legacy records awaiting migration.
			err := a.DB.First(&p, "organization_id = ? AND account_id = ?", org, c.AccountID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
		} else {
			continue
		}
		clock, err := inboxPolicyClock(p)
		if err != nil {
			return nil, err
		}
		byCasePolicy[c.ID] = configured{p, clock, name}
	}
	var caseIDs []uuid.UUID
	for _, c := range cases {
		if _, ok := byCasePolicy[c.ID]; ok {
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
		if entry, ok := byCasePolicy[c.ID]; ok {
			results[c.ID] = inboxSnapshot(entry.clock, entry.policy, c, byCase[c.ID], now)
			results[c.ID].PolicyName = entry.name
			results[c.ID].SnoozeOptions = inboxSnoozeOptions(entry.policy, now)
		}
	}
	return results, nil
}

func validateInboxPolicy(p models.InboxSLAPolicy) error {
	if p.Timezone != "" {
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return errors.New("choose a valid IANA timezone")
		}
	}
	for _, value := range []int{p.FirstReplyMinutes, p.NextReplyMinutes, p.ResolutionMinutes} {
		if value < 1 || value > 43200 {
			return errors.New("SLA targets must be between 1 and 43200 minutes")
		}
	}
	_, err := inboxPolicyClock(p)
	return err
}

func (a *App) ListInboxSLAPolicies(r *fastglue.Request) error {
	org, _, err := a.requireInboxAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
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
	org, _, err := a.requireInboxAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
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
	var payload struct {
		models.InboxSLAPolicy
		Calendar json.RawMessage `json:"date_exceptions"`
	}
	if a.decodeRequest(r, &payload) != nil {
		return nil
	}
	input := payload.InboxSLAPolicy
	if len(payload.Calendar) > 0 {
		if err := json.Unmarshal(payload.Calendar, &input.DateExceptions); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "calendar date exceptions must be a list", nil, "")
		}
	}
	input.OrganizationID, input.AccountID = org, account
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	var invalid error
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		if len(payload.Calendar) == 0 {
			// A client that does not know this additive field must not silently
			// erase an existing calendar while changing other target settings.
			var existing models.InboxSLAPolicy
			err := tx.Select("date_exceptions").First(&existing, "organization_id=? AND account_id=?", org, account).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			input.DateExceptions = existing.DateExceptions
		}
		if invalid = validateInboxPolicy(input); invalid != nil {
			return invalid
		}
		input.VersionID = nil
		version := models.InboxSLAVersion{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, Policy: input}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		input.VersionID = &version.ID
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&input).Error
	}); err != nil {
		if invalid != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, invalid.Error(), nil, "")
		}
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"policy": input})
}

func (a *App) DeleteInboxSLAPolicy(r *fastglue.Request) error {
	org, _, err := a.requireInboxAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	account, err := parsePathUUID(r, "account_id", "WhatsApp account")
	if err != nil {
		return nil
	}
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		return tx.Where("organization_id = ? AND account_id = ?", org, account).Delete(&models.InboxSLAPolicy{}).Error
	}); err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"deleted": true})
}

func (a *App) GetInboxSLAReport(r *fastglue.Request) error {
	org, user, err := a.requireInboxAuth(r, models.ResourceAnalyticsAgents, models.ActionRead)
	if err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, org, false)
	if err != nil || !enabled {
		return a.inboxError(r, errInboxDisabled)
	}
	now := time.Now().UTC()
	q := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).
		Where("inbox_cases.status <> ?", models.InboxCaseResolved).
		Joins("JOIN (SELECT case_id, MIN(deadline) AS next_due, BOOL_OR(deadline <= ?) AS breached, BOOL_OR(NOT paused AND at_risk_at <= ?) AS at_risk FROM inbox_sla_obligations WHERE organization_id = ? AND completed_at IS NULL AND deleted_at IS NULL GROUP BY case_id) risk ON risk.case_id = inbox_cases.id", now, now, org)
	for _, filter := range []string{"account_id", "team_id"} {
		if raw := r.RequestCtx.QueryArgs().Peek(filter); len(raw) > 0 {
			id, err := uuid.Parse(string(raw))
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid inbox report filter", nil, "")
			}
			q = q.Where("inbox_cases."+filter+" = ?", id)
		}
	}
	summary := struct {
		Total    int64 `json:"total"`
		Breached int64 `json:"breached"`
		AtRisk   int64 `json:"at_risk"`
	}{}
	if err := q.Session(&gorm.Session{}).Select("COUNT(*) AS total, COUNT(*) FILTER (WHERE risk.breached) AS breached, COUNT(*) FILTER (WHERE NOT COALESCE(risk.breached,false) AND risk.at_risk) AS at_risk").Scan(&summary).Error; err != nil {
		return a.inboxError(r, err)
	}
	if string(r.RequestCtx.QueryArgs().Peek("all")) != "true" {
		q = q.Where("risk.breached OR risk.at_risk")
	}
	rows := []inboxQueueRow{}
	if err := q.Select(inboxQueueColumns).Joins(inboxQueueOwnerJoin).Joins(inboxQueueTeamJoin).
		Order("risk.breached DESC NULLS LAST, risk.next_due ASC NULLS LAST, inbox_cases.opened_at, inbox_cases.id").Offset(parseInboxQueuePage(r.RequestCtx.QueryArgs())).Limit(51).Scan(&rows).Error; err != nil {
		return a.inboxError(r, err)
	}
	rows, more, err := a.prepareInboxRows(org, rows)
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"cases": rows, "has_more": more, "summary": summary, "updated_at": now})
}
