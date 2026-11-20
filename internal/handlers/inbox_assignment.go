package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

func (a *App) InboxAssignmentOptions(r *fastglue.Request) error {
	t, user, err := a.inboxRequest(r, models.ActionRead)
	if err != nil {
		return nil
	}
	if _, _, err := a.requireAuth(r, models.ResourceContacts, models.ActionWrite); err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, t.org, false)
	if err != nil {
		return a.inboxError(r, err)
	}
	if !enabled {
		return a.inboxError(r, errInboxDisabled)
	}
	c, err := t.latest(a.DB)
	if err != nil {
		return a.inboxError(r, err)
	}
	if err := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, t.org).First(&models.InboxCase{}, "inbox_cases.id=?", c.ID).Error; err != nil {
		return a.inboxError(r, err)
	}
	q := a.DB.Model(&models.User{}).Joins("JOIN user_organizations uo ON uo.user_id=users.id AND uo.organization_id=?", t.org).Joins("LEFT JOIN inbox_agent_capacities lim ON lim.agent_id=users.id AND lim.organization_id=? AND lim.account_id=?", t.org, t.account).Where("users.is_active")
	if c.TeamID != nil {
		q = q.Where("EXISTS (SELECT 1 FROM team_members tm WHERE tm.team_id=? AND tm.user_id=users.id AND tm.deleted_at IS NULL)", *c.TeamID)
	}
	rows := []InboxCapacityRow{}
	if err := q.Select("users.id AS agent_id,users.full_name AS name,users.is_available AS available,COALESCE(lim.max_active,0) AS max_active,(SELECT COUNT(*) FROM inbox_cases work WHERE work.organization_id=? AND work.account_id=? AND work.assignee_id=users.id AND work.id<>? AND work.status IN ? AND work.deleted_at IS NULL) AS active", t.org, t.account, c.ID, inboxActiveStatuses).Order("users.full_name,users.id").Scan(&rows).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"case": c, "agents": rows, "can_override": a.HasPermission(user, models.ResourceTeams, models.ActionWrite, t.org)})
}
func (a *App) AssignInboxConversation(r *fastglue.Request) error {
	t, user, err := a.inboxRequest(r, models.ActionWrite)
	if err != nil {
		return nil
	}
	if _, _, err := a.requireAuth(r, models.ResourceContacts, models.ActionWrite); err != nil {
		return nil
	}
	var input struct {
		CaseID         uuid.UUID  `json:"case_id"`
		Version        int64      `json:"version"`
		AssigneeID     *uuid.UUID `json:"assignee_id"`
		OverrideReason string     `json:"override_reason"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.OverrideReason = strings.TrimSpace(input.OverrideReason)
	if len(input.OverrideReason) > 500 || input.OverrideReason != "" && !a.HasPermission(user, models.ResourceTeams, models.ActionWrite, t.org) {
		return a.inboxError(r, errInboxTransition)
	}
	var c models.InboxCase
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, t.org, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxDisabled
		}
		if err := t.lock(tx); err != nil {
			return err
		}
		c, err = t.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		if c.ID != input.CaseID || c.Version != input.Version || c.Status == models.InboxCaseResolved {
			return errInboxConflict
		}
		if err := a.scopeInboxCases(tx.Model(&models.InboxCase{}), user, t.org).First(&models.InboxCase{}, "inbox_cases.id=?", c.ID).Error; err != nil {
			return err
		}
		if input.AssigneeID != nil {
			return assignInboxCase(tx, &c, user, *input.AssigneeID, input.OverrideReason)
		}
		c.AssigneeID, c.RoutingNextAttemptAt = nil, nil
		c.RoutingReason = ""
		c.Version++
		if err := tx.Save(&c).Error; err != nil {
			return err
		}
		return appendInboxActivity(tx, c, models.InboxCaseActivity{Kind: "unassigned", ActorID: &user})
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"case": c})
}
