package handlers

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type inboxSLAHistoryRow struct {
	models.InboxSLAObligation
	ContactID   uuid.UUID `json:"contact_id"`
	ContactName string    `json:"contact_name"`
	AccountName string    `json:"account_name"`
	TeamName    string    `json:"team_name"`
	AgentName   string    `json:"agent_name"`
}

// Historical compliance only includes completed cycles. Live outstanding work
// is reported separately and never counted as a completed success or failure.
func (a *App) GetInboxSLAHistory(r *fastglue.Request) error {
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
	args := r.RequestCtx.QueryArgs()
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -30)
	for _, input := range []struct {
		key   string
		value *time.Time
	}{{"from", &start}, {"to", &end}} {
		if raw := string(args.Peek(input.key)); raw != "" {
			parsed, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Use an ISO date and explicit timezone for report dates", nil, "")
			}
			*input.value = parsed
		}
	}
	if !start.Before(end) || end.Sub(start) > 366*24*time.Hour {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose a report period of up to 366 days", nil, "")
	}
	cases := a.scopeInboxCaseHistory(a.DB.Model(&models.InboxCase{}), user, org).Select("inbox_cases.id")
	q := a.DB.Model(&models.InboxSLAObligation{}).Where("inbox_sla_obligations.organization_id = ? AND case_id IN (?) AND completed_at >= ? AND completed_at < ?", org, cases, start, end)
	for _, key := range []string{"account_id", "team_id", "agent_id", "policy_id"} {
		if raw := string(args.Peek(key)); raw != "" {
			id, e := uuid.Parse(raw)
			if e != nil {
				return a.inboxError(r, errInboxTransition)
			}
			q = q.Where("inbox_sla_obligations."+key+" = ?", id)
		}
	}
	if kind := string(args.Peek("kind")); kind != "" {
		if kind != "first_reply" && kind != "next_reply" && kind != "resolution" {
			return a.inboxError(r, errInboxTransition)
		}
		q = q.Where("inbox_sla_obligations.kind = ?", kind)
	}
	var summary struct {
		Total      int64 `json:"total"`
		Met        int64 `json:"met"`
		Breached   int64 `json:"breached"`
		Unanswered int64 `json:"unanswered"`
		Legacy     int64 `json:"legacy"`
	}
	if err := q.Session(&gorm.Session{}).Select("COUNT(*) AS total,COUNT(*) FILTER (WHERE outcome='met') AS met,COUNT(*) FILTER (WHERE outcome='breached') AS breached,COUNT(*) FILTER (WHERE outcome='unanswered') AS unanswered,COUNT(*) FILTER (WHERE legacy) AS legacy").Scan(&summary).Error; err != nil {
		return a.inboxError(r, err)
	}
	export := string(args.Peek("format")) == "csv"
	limit := 51
	if export {
		limit = 10001
	}
	rows := []inboxSLAHistoryRow{}
	query := q.Session(&gorm.Session{}).Select("inbox_sla_obligations.*, ic.contact_id, c.profile_name AS contact_name, wa.name AS account_name, COALESCE(t.name,'') AS team_name, COALESCE(u.full_name,'') AS agent_name").
		Joins("JOIN inbox_cases ic ON ic.id=inbox_sla_obligations.case_id AND ic.organization_id=inbox_sla_obligations.organization_id AND ic.deleted_at IS NULL").
		Joins("JOIN contacts c ON c.id=ic.contact_id AND c.organization_id=ic.organization_id AND c.deleted_at IS NULL").
		Joins("JOIN whatsapp_accounts wa ON wa.id=ic.account_id AND wa.organization_id=ic.organization_id AND wa.deleted_at IS NULL").
		Joins("LEFT JOIN teams t ON t.id=inbox_sla_obligations.team_id AND t.organization_id=ic.organization_id AND t.deleted_at IS NULL").
		Joins("LEFT JOIN users u ON u.id=inbox_sla_obligations.agent_id AND u.deleted_at IS NULL AND EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.user_id=u.id AND uo.organization_id=ic.organization_id)").
		Order("completed_at DESC,inbox_sla_obligations.id").Limit(limit)
	if !export {
		query = query.Offset(parseInboxQueuePage(args))
	}
	if err := query.Scan(&rows).Error; err != nil {
		return a.inboxError(r, err)
	}
	for i := range rows {
		if a.ShouldMaskPhoneNumbers(org) {
			rows[i].ContactName = utils.MaskIfPhoneNumber(rows[i].ContactName)
		}
	}
	if export {
		if len(rows) > 10000 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Narrow the report period or filters to export at most 10000 completed targets", nil, "")
		}
		var body bytes.Buffer
		writer := csv.NewWriter(&body)
		_ = writer.Write([]string{"Case", "Contact", "Account", "Team", "Agent", "Policy version", "Target", "Started (UTC)", "Due (UTC)", "Completed (UTC)", "Working seconds", "Target minutes", "Outcome", "Legacy snapshot"})
		for _, row := range rows {
			due := ""
			if row.Deadline != nil {
				due = row.Deadline.UTC().Format(time.RFC3339)
			}
			record := []string{row.CaseID.String(), row.ContactName, row.AccountName, row.TeamName, row.AgentName, row.PolicyID.String(), row.Kind, row.StartedAt.UTC().Format(time.RFC3339), due, row.CompletedAt.UTC().Format(time.RFC3339), strconv.FormatInt(row.WorkingSeconds, 10), strconv.Itoa(row.BudgetMinutes), row.Outcome, strconv.FormatBool(row.Legacy)}
			for i, value := range record {
				trimmed := strings.TrimLeft(value, " \t\r\n")
				if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
					record[i] = "'" + value
				}
			}
			if err := writer.Write(record); err != nil {
				return a.inboxError(r, err)
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return a.inboxError(r, err)
		}
		r.RequestCtx.Response.Header.Set("Content-Type", "text/csv; charset=utf-8")
		r.RequestCtx.Response.Header.Set("Content-Disposition", "attachment; filename=\"inbox-response-targets.csv\"")
		r.RequestCtx.SetBody(body.Bytes())
		return nil
	}
	more := len(rows) > 50
	if more {
		rows = rows[:50]
	}
	var combinations []struct {
		AgentID  *uuid.UUID
		PolicyID uuid.UUID
	}
	if err := q.Session(&gorm.Session{}).Select("DISTINCT agent_id,policy_id").Scan(&combinations).Error; err != nil {
		return a.inboxError(r, err)
	}
	agentIDs := []uuid.UUID{}
	policyIDs := []uuid.UUID{}
	for _, item := range combinations {
		if item.AgentID != nil {
			agentIDs = append(agentIDs, *item.AgentID)
		}
		policyIDs = append(policyIDs, item.PolicyID)
	}
	agents := []struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}{}
	if len(agentIDs) > 0 {
		if err := a.DB.Model(&models.User{}).Select("id,full_name AS name").Where("id IN ? AND EXISTS(SELECT 1 FROM user_organizations uo WHERE uo.user_id=users.id AND uo.organization_id=?)", agentIDs, org).Order("full_name,id").Scan(&agents).Error; err != nil {
			return a.inboxError(r, err)
		}
	}
	versions := []struct {
		ID        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		Legacy    bool      `json:"legacy"`
	}{}
	if len(policyIDs) > 0 {
		if err := a.DB.Model(&models.InboxSLAVersion{}).Select("id,created_at,legacy").Where("organization_id=? AND id IN ?", org, policyIDs).Order("created_at DESC,id").Scan(&versions).Error; err != nil {
			return a.inboxError(r, err)
		}
	}
	return r.SendEnvelope(map[string]any{"results": rows, "summary": summary, "has_more": more, "from": start, "to": end, "updated_at": time.Now().UTC(), "filters": map[string]any{"agents": agents, "policy_versions": versions}})
}
