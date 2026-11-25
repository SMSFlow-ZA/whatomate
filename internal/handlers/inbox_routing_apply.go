package handlers

import (
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxRoutingApplication struct {
	AccountID    uuid.UUID   `json:"account_id"`
	TeamID       uuid.UUID   `json:"team_id"`
	AgentIDs     []uuid.UUID `json:"agent_ids"`
	Revision     int64       `json:"revision"`
	ExpiresAt    time.Time   `json:"expires_at"`
	Confirmation string      `json:"confirmation"`
}

type inboxRoutingChange struct {
	AgentID              uuid.UUID  `json:"agent_id"`
	Name                 string     `json:"name"`
	BeforeActive         int        `json:"before_active"`
	AfterActive          int        `json:"after_active"`
	BeforeAssignments    int        `json:"before_assignments"`
	AfterAssignments     int        `json:"after_assignments"`
	BeforeWindowSeconds  int        `json:"before_window_seconds"`
	AfterWindowSeconds   int        `json:"after_window_seconds"`
	RateRevision         int64      `json:"-"`
	TeamMembershipID     uuid.UUID  `json:"-"`
	OrganisationMemberID uuid.UUID  `json:"-"`
	MemberRoleID         *uuid.UUID `json:"-"`
}

func routingApplicationAgents(input []uuid.UUID) ([]uuid.UUID, error) {
	if len(input) == 0 || len(input) > 100 {
		return nil, errInboxTransition
	}
	ids := append([]uuid.UUID(nil), input...)
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for i, id := range ids {
		if id == uuid.Nil || i > 0 && ids[i-1] == id {
			return nil, errInboxTransition
		}
	}
	return ids, nil
}

func inboxRoutingApplicationChanges(db *gorm.DB, org uuid.UUID, input inboxRoutingApplication, policy models.InboxRoutingPolicy) ([]inboxRoutingChange, error) {
	rows := []inboxRoutingChange{}
	err := db.Table("team_members tm").
		Joins("JOIN users u ON u.id=tm.user_id AND u.is_active AND u.deleted_at IS NULL").
		Joins("JOIN user_organizations uo ON uo.user_id=u.id AND uo.organization_id=? AND uo.deleted_at IS NULL", org).
		Joins("LEFT JOIN inbox_agent_capacities cap ON cap.organization_id=? AND cap.account_id=? AND cap.agent_id=u.id", org, input.AccountID).
		Joins("LEFT JOIN inbox_agent_assignment_rates rate ON rate.organization_id=? AND rate.account_id=? AND rate.agent_id=u.id", org, input.AccountID).
		Where("tm.team_id=? AND tm.user_id IN ? AND tm.deleted_at IS NULL", input.TeamID, input.AgentIDs).
		Select("u.id AS agent_id,u.full_name AS name,tm.id AS team_membership_id,uo.id AS organisation_member_id,uo.role_id AS member_role_id,COALESCE(cap.max_active,0) AS before_active,COALESCE(rate.max_assignments,0) AS before_assignments,COALESCE(rate.window_seconds,0) AS before_window_seconds,COALESCE(rate.revision,0) AS rate_revision").
		Order("u.id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) != len(input.AgentIDs) {
		return nil, errInboxTransition
	}
	for i := range rows {
		row := &rows[i]
		if !models.ValidInboxAssignmentRate(row.BeforeAssignments, row.BeforeWindowSeconds) {
			return nil, errInboxRoutingConfig
		}
		row.AfterActive = row.BeforeActive
		if policy.MaxActive != nil {
			row.AfterActive = *policy.MaxActive
		}
		row.AfterAssignments, row.AfterWindowSeconds = policy.MaxAssignments, policy.WindowSeconds
	}
	return rows, nil
}

func routingChangeFences(changes []inboxRoutingChange) []any {
	fences := make([]any, 0, len(changes))
	for _, c := range changes {
		fences = append(fences, []any{c.AgentID, c.Name, c.BeforeActive, c.AfterActive, c.BeforeAssignments, c.AfterAssignments, c.BeforeWindowSeconds, c.AfterWindowSeconds, c.RateRevision, c.TeamMembershipID, c.OrganisationMemberID, c.MemberRoleID})
	}
	return fences
}

func (a *App) applyInboxRoutingPolicy(r *fastglue.Request, apply bool) error {
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	v, ok := a.inboxRoutingAuthority(r, db, models.ActionWrite)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "policy_id", "routing policy")
	if err != nil {
		return nil
	}
	var input inboxRoutingApplication
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.AgentIDs, err = routingApplicationAgents(input.AgentIDs)
	if err != nil || input.Revision < 1 {
		return a.inboxRoutingError(r, errInboxTransition)
	}
	var current, previous *models.InboxRoutingConfiguration
	var policy models.InboxRoutingPolicy
	var changes []inboxRoutingChange
	var account models.WhatsAppAccount
	var team models.Team
	var expires time.Time
	var effective models.AssignmentStrategy
	var beforeStrategy *models.AssignmentStrategy
	confirmation := ""
	alreadyApplied := false
	err = db.Transaction(func(tx *gorm.DB) error {
		// The same lock guards direct limit edits, routing reservations and this
		// copy operation. A preview holds it briefly but writes no settings.
		if err := lockInboxCapacity(tx, v.Org, input.AccountID); err != nil {
			return err
		}
		fresh, err := loadAuditViewer(tx, v.Org, v.User, nil)
		if err != nil || !fresh.has(models.ResourceTeams, models.ActionWrite) || fresh.fingerprint() != v.fingerprint() {
			if err != nil && !errors.Is(err, errAuditForbidden) && !errors.Is(err, errAuditUnauthenticated) {
				return err
			}
			return errInboxConflict
		}
		account, team, err = inboxRoutingScope(tx, v.Org, input.AccountID, input.TeamID)
		if err != nil {
			return err
		}
		current, err = readInboxRoutingConfiguration(tx, v.Org, input.AccountID, input.TeamID)
		if err != nil {
			return err
		}
		prior := team.AssignmentStrategy
		if current != nil && current.Strategy != models.InboxRoutingInherit {
			prior = current.Strategy
		}
		if models.ValidInboxRoutingStrategy(prior) && prior != models.InboxRoutingInherit {
			beforeStrategy = &prior
		}
		if apply && current != nil && current.PolicyID == id && current.AppliedByID == v.User && inboxPreviewMatches(current.ApplyReceipt, input.Confirmation) {
			// This is recorded completion, not a replay. Other explicit limit
			// changes may have happened since; the client must refresh them.
			alreadyApplied = true
			effective = prior
			return nil
		}
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&policy, "id=? AND organization_id=? AND archived_at IS NULL", id, v.Org).Error; err != nil {
			return err
		}
		if policy.Revision != input.Revision {
			return errInboxConflict
		}
		if policy.Validate() != nil {
			return errInboxRoutingConfig
		}
		effective = policy.Strategy
		if effective == models.InboxRoutingInherit {
			effective = team.AssignmentStrategy
		}
		if !models.ValidInboxRoutingStrategy(effective) || effective == models.InboxRoutingInherit {
			return errInboxRoutingConfig
		}
		changes, err = inboxRoutingApplicationChanges(tx, v.Org, input, policy)
		if err != nil {
			return err
		}
		now, err := inboxRoutingNow(tx)
		if err != nil {
			return err
		}
		expires = now.Add(5 * time.Minute).Truncate(time.Second)
		if apply {
			expires = input.ExpiresAt.UTC()
			if !now.Before(expires) || expires.After(now.Add(5*time.Minute)) {
				return errInboxConflict
			}
		}
		confirmation = inboxPreviewConfirmation([]any{"routing-policy:v1", v.Org, v.User, fresh.fingerprint(), policy, account.ID, account.Name, team.ID, team.Name, team.AssignmentStrategy, team.UpdatedAt, current, routingChangeFences(changes), expires})
		if !apply {
			return nil
		}
		if !inboxPreviewMatches(confirmation, input.Confirmation) {
			return errInboxConflict
		}
		for _, change := range changes {
			if policy.MaxActive != nil {
				limit := models.InboxAgentCapacity{OrganizationID: v.Org, AccountID: input.AccountID, AgentID: change.AgentID, MaxActive: change.AfterActive}
				if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&limit).Error; err != nil {
					return err
				}
			}
			rate := models.InboxAgentAssignmentRate{OrganizationID: v.Org, AccountID: input.AccountID, AgentID: change.AgentID, Revision: change.RateRevision + 1, MaxAssignments: change.AfterAssignments, WindowSeconds: change.AfterWindowSeconds}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&rate).Error; err != nil {
				return err
			}
		}
		revision := int64(1)
		if current != nil {
			previous = current
			revision = current.Revision + 1
		}
		current = &models.InboxRoutingConfiguration{OrganizationID: v.Org, AccountID: input.AccountID, TeamID: input.TeamID, Revision: revision, PolicyID: policy.ID, PolicyRevision: policy.Revision, Name: policy.Name, Strategy: policy.Strategy, AppliedByID: v.User, AppliedAt: now, ApplyReceipt: confirmation}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(current).Error
	})
	if err != nil {
		return a.inboxRoutingError(r, err)
	}
	if apply && !alreadyApplied {
		a.logAudit(v.Org, v.User, "team", input.TeamID, models.AuditActionUpdated, previous, current)
	}
	return r.SendEnvelope(map[string]any{"applied": apply, "already_applied": alreadyApplied, "refresh_current_limits": alreadyApplied, "configuration": current, "policy": policy, "changes": changes, "account_id": input.AccountID, "account_name": account.Name, "team_id": input.TeamID, "team_name": team.Name, "confirmation": confirmation, "expires_at": expires, "limits_shared_across_teams": true, "effective_strategy": effective, "before_strategy": beforeStrategy})
}

func (a *App) PreviewInboxRoutingPolicy(r *fastglue.Request) error {
	return a.applyInboxRoutingPolicy(r, false)
}

func (a *App) ApplyInboxRoutingPolicy(r *fastglue.Request) error {
	return a.applyInboxRoutingPolicy(r, true)
}
