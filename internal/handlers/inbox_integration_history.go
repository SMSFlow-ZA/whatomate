package handlers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// History never decrypts requests/results. Current case/account access and
// author/manager visibility apply before filters, ordering and pagination.
func (a *App) scopedInboxIntegrationHistory(db *gorm.DB, org, actor uuid.UUID) *gorm.DB {
	cases := a.scopeInboxCaseHistory(db.Session(&gorm.Session{NewDB: true}).Model(&models.InboxCase{}), actor, org).Select("inbox_cases.id")
	runs := db.Session(&gorm.Session{NewDB: true}).Model(&models.InboxWorkflowRun{}).Where("organization_id=?", org)
	if !a.HasPermission(actor, models.ResourceSettingsGeneral, models.ActionWrite, org) {
		runs = runs.Where("kind='macro' AND (actor_id=? OR initiated_by_id=?)", actor, actor)
	}
	runs = runs.Where("inbox_workflow_runs.id=inbox_integration_executions.run_id AND inbox_workflow_runs.case_id=inbox_integration_executions.case_id AND inbox_workflow_runs.account_id=inbox_integration_executions.account_id AND inbox_workflow_runs.contact_id=inbox_integration_executions.contact_id")
	return db.Model(&models.InboxIntegrationExecution{}).
		Where("inbox_integration_executions.organization_id=? AND case_id IN (?) AND EXISTS (?)", org, cases, runs.Select("1"))
}

type inboxIntegrationHistoryItem struct {
	ID               uuid.UUID  `json:"id"`
	RunID            uuid.UUID  `json:"run_id"`
	ActionIndex      int        `json:"action_index"`
	IntegrationID    uuid.UUID  `json:"integration_id"`
	CaseID           uuid.UUID  `json:"case_id"`
	AccountID        uuid.UUID  `json:"account_id"`
	ActorID          uuid.UUID  `json:"actor_id"`
	State            string     `json:"state"`
	Outcome          string     `json:"outcome,omitempty"`
	AttemptCount     int        `json:"attempt_count"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	OutputsAppliedAt *time.Time `json:"outputs_applied_at,omitempty"`
	RetryUntil       *time.Time `json:"retry_until,omitempty"`
	ProviderStatus   int        `json:"provider_status,omitempty"`
	DurationMillis   int64      `json:"duration_ms,omitempty"`
}

const integrationHistoryColumns = "id,run_id,action_index,integration_id,case_id,account_id,actor_id,state,outcome,attempt_count,created_at,started_at,completed_at,outputs_applied_at,retry_until,provider_status,duration_millis"

type inboxIntegrationHistoryCursor struct {
	CreatedAt time.Time `json:"at"`
	ID        uuid.UUID `json:"id"`
}

func integrationHistoryPage(r *fastglue.Request, q *gorm.DB) (*gorm.DB, error) {
	// Keyset paging has a fixed query bound and stable ties; an unbounded
	// user-supplied OFFSET must not turn this into a table scan.
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
		q = q.Where("(created_at,id)<(?,?)", cursor.CreatedAt, cursor.ID)
	}
	return q.Order("created_at DESC,id DESC").Limit(51), nil
}

func integrationHistoryCursor(at time.Time, id uuid.UUID) string {
	raw, _ := json.Marshal(inboxIntegrationHistoryCursor{at, id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (a *App) ListInboxIntegrationExecutions(r *fastglue.Request) error {
	org, actor, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	q := a.scopedInboxIntegrationHistory(a.DB, org, actor)
	for _, key := range []string{"case_id", "run_id"} {
		if value := string(r.RequestCtx.QueryArgs().Peek(key)); value != "" {
			id, err := uuid.Parse(value)
			if err != nil || id == uuid.Nil {
				return a.inboxError(r, errInboxTransition)
			}
			q = q.Where(key+"=?", id) // key comes only from this fixed list.
		}
	}
	if state := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("state"))); state != "" {
		switch state {
		case "pending", "submitting", "succeeded", "failed", "unknown", "blocked", "cancelled":
			q = q.Where("state=?", state)
		default:
			return a.inboxError(r, errInboxTransition)
		}
	}
	items := []inboxIntegrationHistoryItem{}
	q, err := integrationHistoryPage(r, q)
	if err != nil {
		return a.inboxError(r, err)
	}
	if err := q.Select(integrationHistoryColumns).Scan(&items).Error; err != nil {
		return a.inboxError(r, err)
	}
	more := len(items) > 50
	next := ""
	if more {
		items = items[:50]
		next = integrationHistoryCursor(items[49].CreatedAt, items[49].ID)
	}
	return r.SendEnvelope(map[string]any{"executions": items, "has_more": more, "next_cursor": next})
}

func (a *App) ListInboxIntegrationRecoveries(r *fastglue.Request) error {
	org, actor, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "execution_id", "Integration execution")
	if err != nil {
		return nil
	}
	var execution models.InboxIntegrationExecution
	if err := a.scopedInboxIntegrationHistory(a.DB, org, actor).First(&execution, "id=?", id).Error; err != nil {
		return a.inboxError(r, err)
	}
	items := []models.InboxIntegrationRecovery{}
	q, err := integrationHistoryPage(r, a.DB.Where("execution_id=? AND organization_id=?", id, org))
	if err != nil {
		return a.inboxError(r, err)
	}
	if err := q.Find(&items).Error; err != nil {
		return a.inboxError(r, err)
	}
	more := len(items) > 50
	next := ""
	if more {
		items = items[:50]
		next = integrationHistoryCursor(items[49].CreatedAt, items[49].ID)
	}
	return r.SendEnvelope(map[string]any{"recoveries": items, "has_more": more, "next_cursor": next})
}
