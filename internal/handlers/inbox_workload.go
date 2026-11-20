package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
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
	loads := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).
		Where("inbox_cases.status IN ? AND inbox_cases.assignee_id IS NOT NULL", inboxActiveStatuses).
		Select("inbox_cases.assignee_id AS agent_id,inbox_cases.account_id,COUNT(*) AS active").
		Group("inbox_cases.assignee_id,inbox_cases.account_id")
	// Include current case owners even when they have no active team. Team
	// memberships remain separate rows; never sum repeated agent/account loads.
	q := a.DB.Table("users u").
		Joins("JOIN user_organizations uo ON uo.deleted_at IS NULL AND uo.user_id=u.id AND uo.organization_id=? AND uo.deleted_at IS NULL", org).
		Joins("LEFT JOIN (SELECT tm.user_id,t.id AS team_id FROM team_members tm JOIN teams t ON t.id=tm.team_id AND t.organization_id=? AND t.is_active AND t.deleted_at IS NULL WHERE tm.deleted_at IS NULL) membership ON membership.user_id=u.id", org).
		Joins("LEFT JOIN teams t ON t.id=membership.team_id").
		Joins("JOIN whatsapp_accounts wa ON wa.organization_id=? AND wa.deleted_at IS NULL", org).
		Joins("LEFT JOIN inbox_agent_capacities lim ON lim.organization_id=? AND lim.account_id=wa.id AND lim.agent_id=u.id", org).
		Joins("LEFT JOIN (?) workload ON workload.agent_id=u.id AND workload.account_id=wa.id", loads).
		Where("u.deleted_at IS NULL AND (t.id IS NOT NULL OR COALESCE(workload.active,0)>0)")
	if !a.HasPermission(user, models.ResourceTeams, models.ActionRead, org) {
		q = q.Where("u.id=?", user)
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
		InboxCapacityRow
		TeamID      uuid.UUID `json:"team_id"`
		TeamName    string    `json:"team_name"`
		AccountID   uuid.UUID `json:"account_id"`
		AccountName string    `json:"account_name"`
	}
	var uniqueAgents int64
	if err := q.Session(&gorm.Session{}).Distinct("u.id").Count(&uniqueAgents).Error; err != nil {
		return a.inboxError(r, err)
	}
	if err := q.Select("u.id AS agent_id,u.full_name AS name,(u.is_active AND u.is_available) AS available, NOT u.is_active AS inactive,COALESCE(t.id,'00000000-0000-0000-0000-000000000000'::uuid) AS team_id,COALESCE(t.name,'') AS team_name,wa.id AS account_id,wa.name AS account_name,COALESCE(lim.max_active,0) AS max_active,COALESCE(workload.active,0) AS active").Order("t.name NULLS LAST,wa.name,u.full_name,u.id").Offset(parseInboxQueuePage(r.RequestCtx.QueryArgs())).Limit(101).Scan(&rows).Error; err != nil {
		return a.inboxError(r, err)
	}
	more := len(rows) > 100
	if more {
		rows = rows[:100]
	}
	return r.SendEnvelope(map[string]any{"agents": rows, "has_more": more, "unique_agents": uniqueAgents, "updated_at": time.Now().UTC()})
}
