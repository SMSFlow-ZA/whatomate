package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"time"
)

func (a *App) GetInboxWorkload(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceAnalyticsAgents, models.ActionRead)
	if err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, org, false)
	if err != nil {
		return a.inboxError(r, err)
	}
	if !enabled {
		return a.inboxError(r, errInboxDisabled)
	}
	scoped := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).Select("inbox_cases.id")
	q := a.DB.Table("team_members tm").Joins("JOIN teams t ON t.id=tm.team_id AND t.organization_id=? AND t.is_active AND t.deleted_at IS NULL", org).
		Joins("JOIN users u ON u.id=tm.user_id AND u.deleted_at IS NULL").Joins("JOIN user_organizations uo ON uo.user_id=u.id AND uo.organization_id=?", org).
		Joins("JOIN whatsapp_accounts wa ON wa.organization_id=? AND wa.deleted_at IS NULL", org).
		Joins("LEFT JOIN inbox_agent_capacities lim ON lim.organization_id=? AND lim.account_id=wa.id AND lim.agent_id=u.id", org).Where("tm.deleted_at IS NULL")
	if !a.HasPermission(user, models.ResourceTeams, models.ActionRead, org) {
		q = q.Where("tm.user_id=?", user)
	}
	if !a.HasPermission(user, models.ResourceContacts, models.ActionRead, org) {
		q = q.Where("wa.id IN (?)", a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).Select("inbox_cases.account_id"))
	}
	for _, key := range []string{"team_id", "account_id"} {
		if raw := string(r.RequestCtx.QueryArgs().Peek(key)); raw != "" {
			id, e := uuid.Parse(raw)
			if e != nil {
				return a.inboxError(r, errInboxTransition)
			}
			column := "t.id"
			if key == "account_id" {
				column = "wa.id"
			}
			q = q.Where(column+"=?", id)
		}
	}
	var rows []struct {
		inboxCapacityRow
		TeamID      uuid.UUID `json:"team_id"`
		TeamName    string    `json:"team_name"`
		AccountID   uuid.UUID `json:"account_id"`
		AccountName string    `json:"account_name"`
	}
	if err := q.Select("u.id AS agent_id,u.full_name AS name,(u.is_active AND u.is_available) AS available, NOT u.is_active AS inactive,t.id AS team_id,t.name AS team_name,wa.id AS account_id,wa.name AS account_name,COALESCE(lim.max_active,0) AS max_active,(SELECT COUNT(*) FROM inbox_cases work WHERE work.id IN (?) AND work.assignee_id=u.id AND work.account_id=wa.id AND work.status IN ? AND work.deleted_at IS NULL) AS active", scoped, inboxActiveStatuses).Order("t.name,wa.name,u.full_name,u.id").Offset(parseInboxQueuePage(r.RequestCtx.QueryArgs())).Limit(101).Scan(&rows).Error; err != nil {
		return a.inboxError(r, err)
	}
	more := len(rows) > 100
	if more {
		rows = rows[:100]
	}
	return r.SendEnvelope(map[string]any{"agents": rows, "has_more": more, "updated_at": time.Now().UTC()})
}
