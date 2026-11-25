package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

var (
	errInboxAssignmentRate    = errors.New("agent is at the assignment rate limit for this WhatsApp account")
	errInboxRoutingConfig     = errors.New("routing configuration needs administrator review")
	errInboxRoutingPermission = errors.New("the agent needs chat read and write permission in this organisation")
	errInboxOverrideForbidden = errors.New("current team administration permission is required to override assignment limits")
)

type inboxAssignmentRateUsage struct {
	AccountID      uuid.UUID  `json:"-"`
	AgentID        uuid.UUID  `json:"agent_id"`
	Revision       int64      `json:"-"`
	MaxAssignments int        `json:"max_assignments"`
	WindowSeconds  int        `json:"window_seconds"`
	Assignments    int64      `json:"assignments"`
	ResumeBase     *time.Time `json:"-"`
	ResumesAt      *time.Time `gorm:"-" json:"resumes_at,omitempty"`
}

// Use the database clock after taking the account lock. A timestamp captured
// before waiting for another worker could exclude that worker's committed claim.
func inboxRoutingNow(db *gorm.DB) (time.Time, error) {
	var now time.Time
	err := db.Raw("SELECT clock_timestamp()").Row().Scan(&now)
	return now.UTC(), err
}

// Read one bounded group, not a per-member query loop. The window index bounds
// each count; the recovery timestamp scans at most the allowed 10000 receipts.
// The Nth newest receipt is intentional: overrides may put a rate above its max,
// so expiry of the oldest receipt alone would not necessarily restore a slot.
func inboxAssignmentRatesAt(db *gorm.DB, org, account uuid.UUID, agents []uuid.UUID, now time.Time) (map[uuid.UUID]inboxAssignmentRateUsage, error) {
	out := make(map[uuid.UUID]inboxAssignmentRateUsage, len(agents))
	pairs := make([]inboxRatePair, 0, len(agents))
	for _, agent := range agents {
		pairs = append(pairs, inboxRatePair{account, agent})
	}
	rows, err := inboxAssignmentRatePairsAt(db, org, pairs, now)
	if err != nil {
		return nil, err
	}
	for key, row := range rows {
		out[key.Agent] = row
	}
	return out, nil
}

type inboxRatePair struct{ Account, Agent uuid.UUID }

func inboxAssignmentRatePairsAt(db *gorm.DB, org uuid.UUID, pairs []inboxRatePair, now time.Time) (map[inboxRatePair]inboxAssignmentRateUsage, error) {
	out := make(map[inboxRatePair]inboxAssignmentRateUsage, len(pairs))
	if len(pairs) == 0 {
		return out, nil
	}
	if len(pairs) > 500 {
		return nil, errInboxRoutingConfig
	}
	keys := make([][]any, 0, len(pairs))
	for _, pair := range pairs {
		keys = append(keys, []any{pair.Account, pair.Agent})
	}
	var rows []inboxAssignmentRateUsage
	err := db.Table("inbox_agent_assignment_rates lim").
		Select(`lim.account_id,lim.agent_id,lim.revision,lim.max_assignments,lim.window_seconds,COUNT(receipt.id) AS assignments,
CASE WHEN lim.max_assignments > 0 AND COUNT(receipt.id) >= lim.max_assignments THEN
 (SELECT recent.assigned_at FROM inbox_assignment_receipts recent
  WHERE recent.organization_id=lim.organization_id AND recent.account_id=lim.account_id AND recent.agent_id=lim.agent_id
    AND recent.assigned_at > ?::timestamptz - make_interval(secs => lim.window_seconds)
  ORDER BY recent.assigned_at DESC,recent.id DESC OFFSET GREATEST(lim.max_assignments-1,0) LIMIT 1)
 ELSE NULL END AS resume_base`, now).
		Joins(`LEFT JOIN inbox_assignment_receipts receipt ON lim.max_assignments>0
 AND receipt.organization_id=lim.organization_id AND receipt.account_id=lim.account_id AND receipt.agent_id=lim.agent_id
 AND receipt.assigned_at > ?::timestamptz - make_interval(secs => lim.window_seconds)`, now).
		Where("lim.organization_id=? AND (lim.account_id,lim.agent_id) IN ?", org, keys).
		Group("lim.organization_id,lim.account_id,lim.agent_id,lim.revision,lim.max_assignments,lim.window_seconds").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Revision < 1 || !models.ValidInboxAssignmentRate(row.MaxAssignments, row.WindowSeconds) {
			return nil, errInboxRoutingConfig
		}
		if row.ResumeBase != nil {
			until := row.ResumeBase.UTC().Add(time.Duration(row.WindowSeconds) * time.Second)
			row.ResumesAt = &until
		}
		out[inboxRatePair{row.AccountID, row.AgentID}] = row
	}
	return out, nil
}

// This guard is read-only. A committed ownership change creates its receipt in
// the database trigger, while retries and transaction rollbacks create none.
func reserveInboxRoutingAssignment(tx *gorm.DB, c models.InboxCase, agent uuid.UUID) error {
	if err := lockInboxCapacity(tx, c.OrganizationID, c.AccountID); err != nil {
		return err
	}
	if c.AssigneeID != nil && *c.AssigneeID == agent {
		var alreadyAssigned int64
		if err := tx.Model(&models.InboxCase{}).Where("id=? AND organization_id=? AND account_id=? AND assignee_id=?", c.ID, c.OrganizationID, c.AccountID, agent).Count(&alreadyAssigned).Error; err != nil {
			return err
		}
		if alreadyAssigned == 1 {
			return nil
		}
	}
	if err := reserveInboxAgent(tx, c, agent); err != nil {
		return err
	}
	now, err := inboxRoutingNow(tx)
	if err != nil {
		return err
	}
	rates, err := inboxAssignmentRatesAt(tx, c.OrganizationID, c.AccountID, []uuid.UUID{agent}, now)
	if err != nil {
		return err
	}
	rate := rates[agent]
	if rate.MaxAssignments > 0 && rate.Assignments >= int64(rate.MaxAssignments) {
		return errInboxAssignmentRate
	}
	return nil
}

func inboxRoutingAssignmentPreflight(db *gorm.DB, c models.InboxCase, agent uuid.UUID, now time.Time, planned ...map[uuid.UUID]int64) (string, error) {
	if err := inboxNamedAssignmentAuthority(db, c, agent); err != nil {
		if errors.Is(err, errInboxRoutingPermission) {
			return "agent_permission_required", nil
		}
		return "", err
	}
	if c.AssigneeID != nil && *c.AssigneeID == agent {
		return "", nil
	}
	full, err := inboxAgentFull(db, c, agent)
	if err != nil {
		return "", err
	}
	if full {
		return "agent_at_capacity", nil
	}
	rates, err := inboxAssignmentRatesAt(db, c.OrganizationID, c.AccountID, []uuid.UUID{agent}, now)
	if err != nil {
		return "", err
	}
	rate := rates[agent]
	additional := int64(0)
	if len(planned) > 0 {
		additional = planned[0][agent]
	}
	if rate.MaxAssignments > 0 && rate.Assignments+additional >= int64(rate.MaxAssignments) {
		return "agent_rate_limited", nil
	}
	return "", nil
}
