// Package inboxquality maintains inbox projections without sending messages.
package inboxquality

import (
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxsla"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func PolicyClock(p models.InboxSLAPolicy) (inboxsla.Clock, error) {
	if p.BusinessHours {
		return inboxsla.BusinessClock(p.Timezone, p.Shifts)
	}
	return inboxsla.ElapsedClock(), nil
}

func PolicyLock(tx *gorm.DB, org, account uuid.UUID) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-sla:"+org.String()+":"+account.String()).Error
}

func EnsureVersion(tx *gorm.DB, p *models.InboxSLAPolicy, legacy bool) error {
	if p.VersionID != nil {
		var existing models.InboxSLAVersion
		if err := tx.First(&existing, "id=? AND organization_id=? AND account_id=?", p.VersionID, p.OrganizationID, p.AccountID).Error; err != nil {
			return err
		}
		known := existing.Policy
		if known.FirstReplyMinutes == p.FirstReplyMinutes && known.NextReplyMinutes == p.NextReplyMinutes && known.ResolutionMinutes == p.ResolutionMinutes && known.Timezone == p.Timezone && known.BusinessHours == p.BusinessHours && slices.Equal(known.Shifts, p.Shifts) {
			return nil
		}
		// An older application can edit the active policy without knowing about
		// version references. Snapshot that edit for future cases, preserving
		// every policy already assigned to a case.
	}
	snapshot := *p
	snapshot.VersionID = nil
	v := models.InboxSLAVersion{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: p.OrganizationID, AccountID: p.AccountID, Policy: snapshot, Legacy: legacy}
	if err := tx.Create(&v).Error; err != nil {
		return err
	}
	p.VersionID = &v.ID
	return tx.Model(&models.InboxSLAPolicy{}).Where("organization_id = ? AND account_id = ?", p.OrganizationID, p.AccountID).Update("version_id", v.ID).Error
}

func AssignPolicy(tx *gorm.DB, c *models.InboxCase, legacy bool) error {
	if c.SLAPolicyAssigned {
		return nil
	}
	if err := PolicyLock(tx, c.OrganizationID, c.AccountID); err != nil {
		return err
	}
	var p models.InboxSLAPolicy
	err := tx.First(&p, "organization_id = ? AND account_id = ?", c.OrganizationID, c.AccountID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		if err := EnsureVersion(tx, &p, legacy); err != nil {
			return err
		}
		c.SLAPolicyID = p.VersionID
	}
	c.SLAPolicyAssigned = true
	c.SLALegacy = legacy
	return tx.Model(c).Updates(map[string]any{"sla_policy_id": c.SLAPolicyID, "sla_policy_assigned": true, "sla_legacy": legacy}).Error
}

func Pauses(events []models.InboxCaseActivity, opened time.Time) []inboxsla.Pause {
	var result []inboxsla.Pause
	for _, e := range events {
		if e.FromStatus == e.ToStatus || e.ToStatus == "" {
			continue
		}
		at := e.CreatedAt
		if e.OccurredAt != nil {
			at = *e.OccurredAt
		}
		if at.Before(opened) {
			continue
		}
		if e.ToStatus == models.InboxCaseWaiting {
			result = append(result, inboxsla.Pause{Start: at})
		} else if e.FromStatus == models.InboxCaseWaiting && len(result) > 0 {
			result[len(result)-1].End = at
		}
	}
	return result
}

// Due merges waiting periods and never shifts an already expired deadline.
func Due(clock inboxsla.Clock, start time.Time, budget time.Duration, pauses []inboxsla.Pause) *time.Time {
	ordered := append([]inboxsla.Pause(nil), pauses...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start.Before(ordered[j].Start) })
	cursor := start
	for _, p := range ordered {
		if !p.End.IsZero() && !p.End.After(cursor) {
			continue
		}
		at := p.Start
		if at.Before(cursor) {
			at = cursor
		}
		due := clock.Deadline(cursor, budget)
		if !due.After(at) {
			return &due
		}
		budget -= clock.Elapsed(cursor, at, nil)
		if p.End.IsZero() {
			return nil
		}
		cursor = p.End
	}
	due := clock.Deadline(cursor, budget)
	return &due
}

func Sync(tx *gorm.DB, c *models.InboxCase, now time.Time) error {
	return syncCase(tx, c, now, false)
}

func syncCase(tx *gorm.DB, c *models.InboxCase, now time.Time, recovering bool) error {
	// Activity producers may supply identifiers only. Never project from a stale
	// or partial struct, or reassign a previously attached response policy.
	var current models.InboxCase
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ? AND organization_id = ?", c.ID, c.OrganizationID).Error; err != nil {
		return err
	}
	*c = current
	if c.OpenedAt.IsZero() {
		return errors.New("case queue entry time is missing")
	}

	if err := AssignPolicy(tx, c, false); err != nil {
		return err
	}
	finish := func() error {
		return tx.Model(&models.InboxCase{}).Where("id=? AND organization_id=?", c.ID, c.OrganizationID).Update("sla_projection_version", c.Version).Error
	}
	if c.SLAPolicyID == nil {
		return finish()
	}
	var v models.InboxSLAVersion
	if err := tx.First(&v, "id = ? AND organization_id = ?", c.SLAPolicyID, c.OrganizationID).Error; err != nil {
		return err
	}
	clock, err := PolicyClock(v.Policy)
	if err != nil {
		return err
	}
	var events []models.InboxCaseActivity
	if err := tx.Select("created_at", "occurred_at", "from_status", "to_status", "id").Where("organization_id = ? AND case_id = ? AND (from_status = ? OR to_status = ?)", c.OrganizationID, c.ID, models.InboxCaseWaiting, models.InboxCaseWaiting).Order("COALESCE(occurred_at,created_at),created_at,id").Find(&events).Error; err != nil {
		return err
	}
	pauses := Pauses(events, c.OpenedAt)
	reconstructing := false
	write := func(kind string, start time.Time, minutes int, stop *time.Time, waiting []inboxsla.Pause, unanswered bool) error {
		if minutes <= 0 {
			return nil
		}
		row := models.InboxSLAObligation{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: c.OrganizationID, AccountID: c.AccountID, CaseID: c.ID, PolicyID: v.ID, Kind: kind, StartedAt: start, BudgetMinutes: minutes, TeamID: c.TeamID, AgentID: c.AssigneeID, Legacy: c.SLALegacy}
		if reconstructing {
			// Timing can be recovered from durable events, but historical ownership
			// is not established by the current case assignment.
			row.Legacy, row.TeamID, row.AgentID = true, nil, nil
		}
		row.Deadline = Due(clock, start, time.Duration(minutes)*time.Minute, waiting)
		row.AtRiskAt = Due(clock, start, max(0, time.Duration(minutes)*time.Minute-max(time.Duration(minutes)*time.Minute/5, 5*time.Minute)), waiting)
		row.Paused = row.Deadline == nil
		if stop != nil {
			row.CompletedAt = stop
			row.Paused = false
			elapsed := clock.Elapsed(start, *stop, waiting)
			row.WorkingSeconds = int64(elapsed / time.Second)
			row.Outcome = "met"
			if elapsed > time.Duration(minutes)*time.Minute {
				row.Outcome = "breached"
			}
			if unanswered {
				row.Outcome = "unanswered"
			}
		}
		// Completed results do not change after later assignments or policy edits.
		updates := clause.AssignmentColumns([]string{"deadline", "at_risk_at", "completed_at", "paused", "outcome", "working_seconds", "team_id", "agent_id", "updated_at"})
		updates = append(updates, clause.Assignment{Column: clause.Column{Name: "legacy"}, Value: gorm.Expr("inbox_sla_obligations.legacy OR EXCLUDED.legacy")})
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "organization_id"}, {Name: "case_id"}, {Name: "kind"}, {Name: "started_at"}}, DoUpdates: updates, Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "inbox_sla_obligations.completed_at IS NULL"}}}}).Create(&row).Error
	}
	firstStop := c.FirstHumanReplyAt
	unanswered := false
	if firstStop == nil && c.ResolvedAt != nil {
		firstStop = c.ResolvedAt
		unanswered = true
	}
	if err := write("first_reply", c.OpenedAt, v.Policy.FirstReplyMinutes, firstStop, nil, unanswered); err != nil {
		return err
	}
	if err := write("resolution", c.OpenedAt, v.Policy.ResolutionMinutes, c.ResolvedAt, pauses, false); err != nil {
		return err
	}
	if recovering {
		var timeline []models.InboxCaseActivity
		if err := tx.Select("created_at", "occurred_at", "kind", "id").Where("organization_id=? AND case_id=? AND kind IN ?", c.OrganizationID, c.ID, []string{"human_reply", "incoming_message", "customer_replied", "opened"}).Order("COALESCE(occurred_at,created_at),created_at,id").Find(&timeline).Error; err != nil {
			return err
		}
		var replied bool
		var pending *time.Time
		reconstructing = true
		for _, event := range timeline {
			at := event.CreatedAt
			if event.OccurredAt != nil {
				at = *event.OccurredAt
			}
			if at.Before(c.OpenedAt) || (c.ResolvedAt != nil && at.After(*c.ResolvedAt)) {
				continue
			}
			if event.Kind == "human_reply" {
				if pending != nil {
					if err := write("next_reply", *pending, v.Policy.NextReplyMinutes, &at, nil, false); err != nil {
						return err
					}
					pending = nil
				}
				replied = true
			} else if replied && pending == nil {
				start := at
				pending = &start
			}
		}
		if pending != nil {
			if err := write("next_reply", *pending, v.Policy.NextReplyMinutes, c.ResolvedAt, nil, c.ResolvedAt != nil); err != nil {
				return err
			}
		}
		reconstructing = false
	}
	var existing []models.InboxSLAObligation
	if err := tx.Where("organization_id = ? AND case_id = ? AND kind = 'next_reply' AND completed_at IS NULL", c.OrganizationID, c.ID).Find(&existing).Error; err != nil {
		return err
	}
	for _, row := range existing {
		stop := c.LastHumanReplyAt
		if stop != nil && !stop.Before(row.StartedAt) {
			if err := write("next_reply", row.StartedAt, row.BudgetMinutes, stop, nil, false); err != nil {
				return err
			}
		} else if c.ResolvedAt != nil {
			if err := write("next_reply", row.StartedAt, row.BudgetMinutes, c.ResolvedAt, nil, true); err != nil {
				return err
			}
		}
	}
	if c.NextReplyFrom != nil {
		if err := write("next_reply", *c.NextReplyFrom, v.Policy.NextReplyMinutes, c.ResolvedAt, nil, c.ResolvedAt != nil); err != nil {
			return err
		}
	}
	return finish()
}

// Backfill records only the policy known at upgrade, explicitly marked legacy.
// It neither synthesizes incoming events nor requeues existing automation work.
func Backfill(db *gorm.DB) error {
	var cursor *uuid.UUID
	for {
		var rows []models.InboxCase
		query := db.Where("(NOT sla_policy_assigned OR sla_projection_version <> version)")
		if cursor != nil {
			query = query.Where("id > ?", *cursor)
		}
		if err := query.Order("id").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, c := range rows {
			id := c.ID
			cursor = &id
			if err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, "id = ? AND organization_id = ?", c.ID, c.OrganizationID).Error; err != nil {
					return err
				}
				if c.SLAPolicyAssigned && c.SLAProjectionVersion == c.Version {
					return nil
				}
				if !c.SLAPolicyAssigned {
					if err := AssignPolicy(tx, &c, true); err != nil {
						return err
					}
				}
				return syncCase(tx, &c, time.Now().UTC(), true)
			}); err != nil {
				return err
			}
		}
	}
}
