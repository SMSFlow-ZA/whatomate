package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errInboxAgentAtCapacity = errors.New("agent is at capacity for this WhatsApp account")
var inboxActiveStatuses = []models.InboxCaseStatus{models.InboxCaseOpen, models.InboxCasePending}

func inboxCountsForCapacity(status models.InboxCaseStatus) bool {
	return status == models.InboxCaseOpen || status == models.InboxCasePending
}

// Use the same account lock for new or edited limits and every transaction
// that adds an active assignment. This also covers limits created concurrently.
func lockInboxCapacity(tx *gorm.DB, org, account uuid.UUID) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-capacity:"+org.String()+":"+account.String()).Error
}

func reserveInboxAgent(tx *gorm.DB, c models.InboxCase, userID uuid.UUID) error {
	if !inboxCountsForCapacity(c.Status) {
		return nil
	}
	if err := lockInboxCapacity(tx, c.OrganizationID, c.AccountID); err != nil {
		return err
	}
	var limit models.InboxAgentCapacity
	err := tx.First(&limit, "organization_id = ? AND account_id = ? AND agent_id = ?", c.OrganizationID, c.AccountID, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil || limit.MaxActive == 0 {
		return err
	}
	var count int64
	if err := tx.Model(&models.InboxCase{}).
		Where("organization_id = ? AND account_id = ? AND assignee_id = ? AND id <> ? AND status IN ?",
			c.OrganizationID, c.AccountID, userID, c.ID, inboxActiveStatuses).
		Count(&count).Error; err != nil {
		return err
	}
	if count >= int64(limit.MaxActive) {
		return errInboxAgentAtCapacity
	}
	return nil
}

func protectInboxActiveAssignment(tx *gorm.DB, c *models.InboxCase) error {
	if c.AssigneeID == nil {
		return nil
	}
	err := inboxNamedAssignmentAuthority(tx, *c, *c.AssigneeID)
	if err == nil {
		err = reserveInboxAgent(tx, *c, *c.AssigneeID)
	}
	if err == nil {
		err = reserveInboxRoutingAssignment(tx, *c, *c.AssigneeID)
	}
	switch {
	case errors.Is(err, errInboxAgentAtCapacity):
		c.AssigneeID, c.RoutingReason = nil, "capacity"
		return nil
	case errors.Is(err, errInboxAssignmentRate):
		c.AssigneeID, c.RoutingReason = nil, "rate_limited"
		return nil
	case errors.Is(err, errInboxRoutingPermission):
		c.AssigneeID, c.RoutingReason = nil, "permission_required"
		return nil
	case errors.Is(err, errInboxRoutingConfig):
		c.AssigneeID, c.RoutingReason = nil, "invalid_config"
		return nil
	default:
		return err
	}
}

// InboxCapacityRow is exported so GORM can populate embedded report rows.
type InboxCapacityRow struct {
	AgentID           uuid.UUID  `json:"agent_id"`
	Name              string     `json:"name"`
	MaxActive         int        `json:"max_active"`
	Active            int64      `json:"active"`
	Available         bool       `json:"available"`
	Inactive          bool       `json:"inactive"`
	MaxAssignments    int        `json:"max_assignments"`
	WindowSeconds     int        `json:"window_seconds"`
	Assignments       int64      `json:"assignments"`
	RateResumesAt     *time.Time `json:"rate_resumes_at,omitempty"`
	RateDetailsHidden bool       `json:"rate_details_hidden"`
	AssignmentReason  string     `json:"assignment_reason,omitempty"`
}

func (a *App) inboxTeamCapacityContext(r *fastglue.Request, action string) (uuid.UUID, uuid.UUID, uuid.UUID, error) {
	org, _, err := a.requireAuth(r, models.ResourceTeams, action)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	team, err := parsePathUUID(r, "team_id", "Team")
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	account, err := parsePathUUID(r, "account_id", "WhatsApp account")
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	if _, err = findByIDAndOrg[models.Team](a.DB, r, team, org, "Team"); err == nil {
		_, err = findByIDAndOrg[models.WhatsAppAccount](a.DB, r, account, org, "WhatsApp account")
	}
	return org, team, account, err
}

func (a *App) GetInboxTeamCapacity(r *fastglue.Request) error {
	org, team, account, err := a.inboxTeamCapacityContext(r, models.ActionRead)
	if err != nil {
		return nil
	}
	rows := []InboxCapacityRow{}
	err = a.DB.Table("team_members tm").
		Joins("JOIN users u ON u.id = tm.user_id AND u.deleted_at IS NULL").
		Joins("JOIN user_organizations uo ON uo.deleted_at IS NULL AND uo.user_id=u.id AND uo.organization_id=?", org).
		Joins("LEFT JOIN inbox_agent_capacities lim ON lim.organization_id = ? AND lim.account_id = ? AND lim.agent_id = tm.user_id", org, account).
		Where("tm.team_id = ? AND tm.deleted_at IS NULL", team).
		Select("u.id AS agent_id, u.full_name AS name, COALESCE(lim.max_active, 0) AS max_active, (u.is_active AND u.is_available) AS available, NOT u.is_active AS inactive, "+
			"(SELECT COUNT(*) FROM inbox_cases ic WHERE ic.organization_id = ? AND ic.account_id = ? AND ic.assignee_id = u.id AND ic.status IN ? AND ic.deleted_at IS NULL) AS active",
			org, account, inboxActiveStatuses).
		Order("u.full_name, u.id").Scan(&rows).Error
	if err != nil {
		return a.inboxError(r, err)
	}
	contexts := make([]inboxRoutingCapacityContext, 0, len(rows))
	for i := range rows {
		contexts = append(contexts, inboxRoutingCapacityContext{Row: &rows[i], Account: account, Team: team, ShowCounts: true})
	}
	if err := decorateInboxRoutingCapacity(a.DB, org, contexts); err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"agents": rows})
}

func (a *App) SetInboxTeamCapacity(r *fastglue.Request) error {
	org, team, account, err := a.inboxTeamCapacityContext(r, models.ActionWrite)
	if err != nil {
		return nil
	}
	var input struct {
		AgentID   uuid.UUID `json:"agent_id"`
		MaxActive *int      `json:"max_active"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if input.AgentID == uuid.Nil || input.MaxActive == nil || *input.MaxActive < 0 || *input.MaxActive > 10000 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Enter a case limit between 0 and 10000", nil, "")
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockInboxCapacity(tx, org, account); err != nil {
			return err
		}
		var member models.TeamMember
		if err := tx.First(&member, "team_id = ? AND user_id = ?", team, input.AgentID).Error; err != nil {
			return err
		}
		limit := models.InboxAgentCapacity{OrganizationID: org, AccountID: account, AgentID: input.AgentID, MaxActive: *input.MaxActive}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&limit).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"saved": true})
}

// Overflow uses the same strategies and availability used by transfers. It
// updates the shared round-robin timestamp only after reserving an inbox slot.
func (a *App) routeInboxOverflow(ctx context.Context) error {
	var cases []models.InboxCase
	err := a.DB.WithContext(ctx).
		Where("assignee_id IS NULL AND team_id IS NOT NULL AND status IN ? AND routing_reason <> ''", inboxActiveStatuses).
		Where(`(routing_reason<>'manual' OR EXISTS (SELECT 1 FROM inbox_routing_configurations cfg JOIN teams team ON team.id=cfg.team_id AND team.organization_id=cfg.organization_id AND team.deleted_at IS NULL
 WHERE cfg.organization_id=inbox_cases.organization_id AND cfg.account_id=inbox_cases.account_id AND cfg.team_id=inbox_cases.team_id
 AND (cfg.strategy IN ('round_robin','load_balanced') OR (cfg.strategy='inherit' AND team.assignment_strategy IN ('round_robin','load_balanced')))))`).
		Where("routing_next_attempt_at IS NULL OR routing_next_attempt_at <= ?", time.Now().UTC()).
		Where("EXISTS (SELECT 1 FROM organizations o WHERE o.id = inbox_cases.organization_id AND o.inbox_workflows_enabled AND o.deleted_at IS NULL)").
		Order("routing_next_attempt_at ASC NULLS FIRST, CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, opened_at, id").
		Limit(100).Find(&cases).Error
	if err != nil {
		return err
	}
	for _, candidate := range cases {
		if err := a.routeOneInboxCase(ctx, candidate); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) routeOneInboxCase(ctx context.Context, candidate models.InboxCase) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		thread := inboxThread{candidate.OrganizationID, candidate.AccountID, candidate.ContactID}
		enabled, err := inboxWorkflowEnabled(tx, thread.org, true)
		if err != nil || !enabled {
			return err
		}
		if err := thread.lock(tx); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tx.Model(&models.InboxCase{}).Where("id = ? AND organization_id = ?", candidate.ID, thread.org).
					Update("routing_next_attempt_at", time.Now().UTC().Add(15*time.Second)).Error
			}
			return err
		}
		c, err := thread.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil || c.ID != candidate.ID || c.AssigneeID != nil || c.TeamID == nil || !inboxCountsForCapacity(c.Status) {
			return err
		}
		// Fair, restart-safe retry rounds stop unavailable teams from occupying
		// every batch. Priority and age determine order within each round.
		next := time.Now().UTC().Add(15 * time.Second)
		c.RoutingNextAttemptAt = &next
		if err := tx.Model(&c).Update("routing_next_attempt_at", next).Error; err != nil {
			return err
		}
		return a.routeEligibleInboxCase(tx, &c)
	})
}

func (a *App) RunInboxOverflow(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := a.routeInboxOverflow(ctx); err != nil && ctx.Err() == nil {
			a.Log.Error("Inbox overflow routing failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Caller holds the thread and case locks. Capacity is reserved atomically.
func (a *App) routeEligibleInboxCase(tx *gorm.DB, c *models.InboxCase) error {
	if err := lockInboxCapacity(tx, c.OrganizationID, c.AccountID); err != nil {
		return err
	}
	plan, err := resolveInboxRoutingPlan(tx, *c)
	if err != nil {
		return err
	}
	now, err := inboxRoutingNow(tx)
	if err != nil {
		return err
	}
	hold := func(reason string) error {
		if c.RoutingReason == reason && reason != "manual" {
			return nil
		}
		c.RoutingReason = reason
		if reason == "manual" {
			c.RoutingNextAttemptAt = nil
		}
		c.Version++
		return tx.Save(c).Error
	}
	if plan.Reason != "" {
		return hold(plan.Reason)
	}
	if plan.Named {
		first, err := inboxRoutingCaseFirst(tx, *c, now)
		if err != nil {
			return err
		}
		if !first {
			return hold("priority_wait")
		}
	}
	m, reason, err := chooseInboxRoutingCandidate(tx, *c, plan, now)
	if err != nil {
		return err
	}
	if reason != "" {
		return hold(reason)
	}
	if err := inboxNamedAssignmentAuthority(tx, *c, m.UserID); err != nil {
		if errors.Is(err, errInboxRoutingPermission) {
			return hold("permission_required")
		}
		return err
	}
	if err := reserveInboxRoutingAssignment(tx, *c, m.UserID); err != nil {
		return err
	}
	c.AssigneeID, c.RoutingReason = &m.UserID, ""
	c.RoutingNextAttemptAt = nil
	c.Version++
	if err := tx.Save(c).Error; err != nil {
		return err
	}
	if err := tx.Model(&m).Update("last_assigned_at", now).Error; err != nil {
		return err
	}
	return appendInboxActivity(tx, *c, models.InboxCaseActivity{Kind: "assigned"})
}
