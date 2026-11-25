package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

type inboxRoutingCapacityContext struct {
	Row           *InboxCapacityRow
	Account, Team uuid.UUID
	CurrentOwner  *uuid.UUID
	ShowCounts    bool
}

// Decorate only already-authorised rows. Batches span accounts and teams, avoiding
// one rate/permission/configuration lookup for every supervisor-report row.
func decorateInboxRoutingCapacity(db *gorm.DB, org uuid.UUID, contexts []inboxRoutingCapacityContext) error {
	for start := 0; start < len(contexts); start += 500 {
		end := min(start+500, len(contexts))
		if err := decorateInboxRoutingCapacityBatch(db, org, contexts[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func decorateInboxRoutingCapacityBatch(db *gorm.DB, org uuid.UUID, contexts []inboxRoutingCapacityContext) error {
	if len(contexts) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(contexts))
	pairs := make([]inboxRatePair, 0, len(contexts))
	teamKeys := make([][]any, 0, len(contexts))
	for _, context := range contexts {
		ids = append(ids, context.Row.AgentID)
		pairs = append(pairs, inboxRatePair{context.Account, context.Row.AgentID})
		if context.Team != uuid.Nil {
			teamKeys = append(teamKeys, []any{context.Account, context.Team})
		}
	}
	now, err := inboxRoutingNow(db)
	if err != nil {
		return err
	}
	rates, err := inboxAssignmentRatePairsAt(db, org, pairs, now)
	if err != nil {
		return err
	}
	var allowed []uuid.UUID
	if err := db.Table("user_organizations uo").Joins("JOIN users u ON u.id=uo.user_id AND u.is_active AND u.deleted_at IS NULL").
		Where("uo.organization_id=? AND uo.user_id IN ? AND uo.deleted_at IS NULL", org, ids).
		Where(inboxRoutingChatRights).Pluck("uo.user_id", &allowed).Error; err != nil {
		return err
	}
	permissions := make(map[uuid.UUID]bool, len(allowed))
	for _, id := range allowed {
		permissions[id] = true
	}
	configs := map[[2]uuid.UUID]models.InboxRoutingConfiguration{}
	if len(teamKeys) > 0 {
		var rows []models.InboxRoutingConfiguration
		if err := db.Where("organization_id=? AND (account_id,team_id) IN ?", org, teamKeys).Find(&rows).Error; err != nil {
			return err
		}
		for _, config := range rows {
			configs[[2]uuid.UUID{config.AccountID, config.TeamID}] = config
		}
	}
	for _, context := range contexts {
		row := context.Row
		rate, hasRate := rates[inboxRatePair{context.Account, row.AgentID}]
		config, named := configs[[2]uuid.UUID{context.Account, context.Team}]
		row.MaxAssignments, row.WindowSeconds = rate.MaxAssignments, rate.WindowSeconds
		row.Assignments, row.RateResumesAt = rate.Assignments, rate.ResumesAt
		row.RateDetailsHidden = !context.ShowCounts
		if !context.ShowCounts {
			row.Assignments = 0
			row.RateResumesAt = nil
		}
		row.AssignmentReason = ""
		switch {
		case named && (!models.ValidInboxRoutingStrategy(config.Strategy) || config.Revision < 1 || config.PolicyRevision < 1):
			row.AssignmentReason = "invalid_config"
		case row.Inactive || !row.Available:
			row.AssignmentReason = "unavailable"
		case (named || hasRate) && !permissions[row.AgentID]:
			row.AssignmentReason = "permission_required"
		case context.CurrentOwner != nil && *context.CurrentOwner == row.AgentID: // Unchanged ownership consumes no new slot.
		case row.MaxActive > 0 && row.Active >= int64(row.MaxActive):
			row.AssignmentReason = "capacity"
		case rate.MaxAssignments > 0 && rate.Assignments >= int64(rate.MaxAssignments):
			row.AssignmentReason = "rate_limited"
		}
	}
	return nil
}
