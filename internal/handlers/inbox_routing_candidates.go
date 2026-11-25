package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

type inboxRoutingPlan struct {
	Team     models.Team
	Strategy models.AssignmentStrategy
	Named    bool
	Reason   string
}

func resolveInboxRoutingPlan(db *gorm.DB, c models.InboxCase) (inboxRoutingPlan, error) {
	plan := inboxRoutingPlan{}
	if c.TeamID == nil {
		plan.Reason = "invalid_config"
		return plan, nil
	}
	if err := db.First(&plan.Team, "id=? AND organization_id=? AND is_active", *c.TeamID, c.OrganizationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			plan.Reason = "invalid_config"
			return plan, nil
		}
		return plan, err
	}
	config, err := readInboxRoutingConfiguration(db, c.OrganizationID, c.AccountID, *c.TeamID)
	if errors.Is(err, errInboxRoutingConfig) {
		plan.Reason = "invalid_config"
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.Strategy, plan.Named = plan.Team.AssignmentStrategy, config != nil
	if config != nil && config.Strategy != models.InboxRoutingInherit {
		plan.Strategy = config.Strategy
	}
	if !models.ValidInboxRoutingStrategy(plan.Strategy) || plan.Strategy == models.InboxRoutingInherit {
		plan.Reason = "invalid_config"
	}
	if plan.Strategy == models.AssignmentStrategyManual {
		plan.Reason = "manual"
	}
	return plan, nil
}

// The membership join below is mandatory even for native/super identities. Role
// permissions are read in this query, never borrowed from an organisation cache.
const inboxRoutingChatRights = `(u.is_super_admin OR (
EXISTS (SELECT 1 FROM custom_roles cr JOIN role_permissions rp ON rp.custom_role_id=cr.id JOIN permissions p ON p.id=rp.permission_id
 WHERE cr.id=uo.role_id AND cr.organization_id=uo.organization_id AND cr.deleted_at IS NULL AND p.resource='chat' AND p.action='read')
AND EXISTS (SELECT 1 FROM custom_roles cr JOIN role_permissions rp ON rp.custom_role_id=cr.id JOIN permissions p ON p.id=rp.permission_id
 WHERE cr.id=uo.role_id AND cr.organization_id=uo.organization_id AND cr.deleted_at IS NULL AND p.resource='chat' AND p.action='write')))`

func inboxRoutingMemberQuery(db *gorm.DB, c models.InboxCase, plan inboxRoutingPlan) *gorm.DB {
	return db.Model(&models.TeamMember{}).
		Joins("JOIN users u ON u.id=team_members.user_id AND u.is_active AND u.is_available AND u.deleted_at IS NULL").
		Joins("JOIN user_organizations uo ON uo.user_id=u.id AND uo.organization_id=? AND uo.deleted_at IS NULL", c.OrganizationID).
		Joins("LEFT JOIN inbox_agent_capacities cap ON cap.organization_id=? AND cap.account_id=? AND cap.agent_id=u.id", c.OrganizationID, c.AccountID).
		Joins("LEFT JOIN inbox_agent_assignment_rates rate ON rate.organization_id=? AND rate.account_id=? AND rate.agent_id=u.id", c.OrganizationID, c.AccountID).
		Where("team_members.team_id=?", plan.Team.ID)
}

func routingCapacityCandidates(q *gorm.DB, c models.InboxCase) *gorm.DB {
	return q.Where(`(COALESCE(cap.max_active,0)=0 OR
 (SELECT COUNT(*) FROM inbox_cases active WHERE active.organization_id=? AND active.account_id=? AND active.assignee_id=u.id
 AND active.id<>? AND active.deleted_at IS NULL AND active.status IN ?) < cap.max_active)`, c.OrganizationID, c.AccountID, c.ID, inboxActiveStatuses)
}

func routingRateCandidates(q *gorm.DB, c models.InboxCase, now time.Time) *gorm.DB {
	return q.Where(`(COALESCE(rate.max_assignments,0)=0 OR
 (SELECT COUNT(*) FROM inbox_assignment_receipts receipt WHERE receipt.organization_id=? AND receipt.account_id=? AND receipt.agent_id=u.id
 AND receipt.assigned_at > ?::timestamptz - make_interval(secs=>rate.window_seconds)) < rate.max_assignments)`, c.OrganizationID, c.AccountID, now)
}

// Filter capacity/rates in PostgreSQL before choosing a member. Large teams do
// not require one permission, workload and rate query for every full agent.
// Caller repeats reservation under the account lock before the actual change.
func chooseInboxRoutingCandidate(db *gorm.DB, c models.InboxCase, plan inboxRoutingPlan, now time.Time) (models.TeamMember, string, error) {
	var candidate models.TeamMember
	if plan.Reason != "" {
		return candidate, plan.Reason, nil
	}
	base := func() *gorm.DB {
		q := inboxRoutingMemberQuery(db, c, plan)
		if plan.Named {
			q = q.Where(inboxRoutingChatRights)
		}
		return q
	}
	var invalid int64
	if err := base().Where(`rate.agent_id IS NOT NULL AND (rate.revision<1 OR rate.max_assignments<0 OR rate.max_assignments>10000 OR
 NOT ((rate.max_assignments=0 AND rate.window_seconds=0) OR (rate.max_assignments>0 AND rate.window_seconds BETWEEN 60 AND 86400)))`).Count(&invalid).Error; err != nil {
		return candidate, "", err
	}
	if invalid > 0 {
		return candidate, "invalid_config", nil
	}
	q := routingRateCandidates(routingCapacityCandidates(base(), c), c, now).Select("team_members.*")
	if plan.Strategy == models.AssignmentStrategyLoadBalanced {
		q = q.Select(`team_members.*,(SELECT COUNT(*) FROM inbox_cases work WHERE work.organization_id=? AND work.account_id=? AND work.assignee_id=u.id AND work.status IN ? AND work.deleted_at IS NULL) AS inbox_load`, c.OrganizationID, c.AccountID, inboxActiveStatuses).Order("inbox_load")
	}
	err := q.Order("team_members.last_assigned_at ASC NULLS FIRST,team_members.user_id").First(&candidate).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return candidate, "", err
	}
	var count int64
	if err := inboxRoutingMemberQuery(db, c, plan).Count(&count).Error; err != nil {
		return candidate, "", err
	}
	if count == 0 {
		return candidate, "unavailable", nil
	}
	if plan.Named {
		if err := base().Count(&count).Error; err != nil {
			return candidate, "", err
		}
		if count == 0 {
			return candidate, "permission_required", nil
		}
	}
	if err := routingCapacityCandidates(base(), c).Count(&count).Error; err != nil {
		return candidate, "", err
	}
	if count == 0 {
		return candidate, "capacity", nil
	}
	return candidate, "rate_limited", nil
}

func inboxNamedAssignmentAuthority(db *gorm.DB, c models.InboxCase, agent uuid.UUID) error {
	var configured int64
	if err := db.Model(&models.InboxAgentAssignmentRate{}).Where("organization_id=? AND account_id=? AND agent_id=?", c.OrganizationID, c.AccountID, agent).Count(&configured).Error; err != nil {
		return err
	}
	if configured == 0 && c.TeamID != nil {
		if err := db.Model(&models.InboxRoutingConfiguration{}).Where("organization_id=? AND account_id=? AND team_id=?", c.OrganizationID, c.AccountID, *c.TeamID).Count(&configured).Error; err != nil {
			return err
		}
	}
	if configured == 0 {
		return nil
	}
	var membership models.UserOrganization
	if err := db.Where("organization_id=? AND user_id=?", c.OrganizationID, agent).First(&membership).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errInboxRoutingPermission
		}
		return err
	}
	v, err := loadAuditViewer(db, c.OrganizationID, agent, nil)
	if errors.Is(err, errAuditForbidden) || errors.Is(err, errAuditUnauthenticated) || err == nil && (!v.has(models.ResourceChat, models.ActionRead) || !v.has(models.ResourceChat, models.ActionWrite)) {
		return errInboxRoutingPermission
	}
	return err
}

// Deterministic order within one named account/team, including concurrent worker
// claims. Retry rounds remain bounded by the existing durable scheduling query.
func inboxRoutingCaseFirst(db *gorm.DB, c models.InboxCase, now time.Time) (bool, error) {
	var first models.InboxCase
	err := db.Select("id").Where("organization_id=? AND account_id=? AND team_id=? AND assignee_id IS NULL AND status IN ? AND routing_reason<>''", c.OrganizationID, c.AccountID, c.TeamID, inboxActiveStatuses).
		Where("(id=? OR routing_next_attempt_at IS NULL OR routing_next_attempt_at<=?)", c.ID, now).
		Where("EXISTS (SELECT 1 FROM contacts contact WHERE contact.id=inbox_cases.contact_id AND contact.organization_id=inbox_cases.organization_id AND contact.deleted_at IS NULL)").
		Where("NOT EXISTS (SELECT 1 FROM inbox_cases newer WHERE newer.organization_id=inbox_cases.organization_id AND newer.account_id=inbox_cases.account_id AND newer.contact_id=inbox_cases.contact_id AND newer.deleted_at IS NULL AND (newer.opened_at,newer.id)>(inbox_cases.opened_at,inbox_cases.id))").
		Order("CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,opened_at,id").First(&first).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return first.ID == c.ID, err
}
