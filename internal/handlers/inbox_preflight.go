package handlers

import (
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Read-only predictions. No case, assignment, capacity, note or message writes
// are made here. Execution repeats authorization and all stateful checks.
func (a *App) preflightInboxActions(db *gorm.DB, w models.InboxWorkflow, c models.InboxCase, user uuid.UUID, now time.Time, integrations ...map[int]preparedInboxIntegration) ([]models.InboxActionOutcome, bool, error) {
	out := make([]models.InboxActionOutcome, 0, len(w.Actions))
	eligible := true
	canAssign := a.HasPermission(user, models.ResourceContacts, models.ActionWrite, c.OrganizationID)
	canWrite := a.HasPermission(user, models.ResourceChat, models.ActionWrite, c.OrganizationID)
	plannedFields := map[uuid.UUID]bool{}
	plannedVariables := map[string]bool{}
	for i, action := range w.Actions {
		item := models.InboxActionOutcome{Index: i, Type: action.Type, Status: "ready", Reason: "will_apply"}
		if !canWrite {
			item.Status, item.Reason = "blocked", "permission_required"
		} else {
			switch action.Type {
			case "integration":
				id, err := uuid.Parse(action.Value)
				if err != nil {
					return nil, false, errInboxTransition
				}
				prepared, err := a.prepareInboxIntegration(db, c, user, id, action.IntegrationInputs, plannedVariables)
				if len(integrations) > 0 {
					integrations[0][i] = prepared
				}
				item.Detail = prepared.Intent.Name
				if err != nil {
					item.Status, item.Reason = "blocked", integrationProblemReason(err)
				} else if !prepared.Intent.Available {
					item.Status, item.Reason = "blocked", prepared.Intent.Reason
				} else {
					item.Reason = "integration_will_execute"
					if len(prepared.Intent.DeferredInputs) != 0 {
						item.Status, item.Reason = "deferred", "prior_integration_result_requires_review"
					}
				}
				if item.Status != "blocked" {
					for _, output := range prepared.Intent.Outputs {
						plannedFields[output.ID] = true
						plannedVariables[output.Key] = true
					}
				}
			case "assign":
				if !canAssign {
					item.Status, item.Reason = "blocked", "assignment_permission_required"
					break
				}
				id, err := uuid.Parse(action.Value)
				if err != nil {
					return nil, false, errInboxTransition
				}
				q := db.Model(&models.UserOrganization{}).Joins("JOIN users u ON u.id=user_organizations.user_id AND u.is_active AND u.deleted_at IS NULL").Where("user_organizations.organization_id=? AND user_organizations.user_id=?", c.OrganizationID, id)
				if c.TeamID != nil {
					q = q.Where("EXISTS (SELECT 1 FROM team_members tm WHERE tm.user_id=u.id AND tm.team_id=? AND tm.deleted_at IS NULL)", *c.TeamID)
				}
				var count int64
				if err := q.Count(&count).Error; err != nil {
					return nil, false, err
				}
				if count == 0 {
					item.Status, item.Reason = "blocked", "agent_ineligible"
					break
				}
				full, err := inboxAgentFull(db, c, id)
				if err != nil {
					return nil, false, err
				}
				if full {
					item.Status, item.Reason = "blocked", "agent_at_capacity"
					break
				}
				var assignee models.User
				if err := db.Select("id,full_name").First(&assignee, "id=?", id).Error; err != nil {
					return nil, false, err
				}
				item.Detail = assignee.FullName
				c.AssigneeID = &id
			case "assign_team":
				if c.Status == models.InboxCaseResolved {
					item.Status, item.Reason = "blocked", "case_resolved"
					break
				}
				if !canAssign {
					item.Status, item.Reason = "blocked", "assignment_permission_required"
					break
				}
				id, err := uuid.Parse(action.Value)
				if err != nil {
					return nil, false, errInboxTransition
				}
				var team models.Team
				if err := db.First(&team, "id=? AND organization_id=? AND is_active", id, c.OrganizationID).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						item.Status, item.Reason = "blocked", "team_unavailable"
						break
					}
					return nil, false, err
				}
				item.Detail = team.Name
				c.TeamID, c.AssigneeID = &id, nil
				if team.AssignmentStrategy == models.AssignmentStrategyManual || !inboxCountsForCapacity(c.Status) {
					item.Status, item.Reason = "unassigned", "manual_assignment_required"
					break
				}
				var members []models.TeamMember
				if err := db.Joins("JOIN users u ON u.id=team_members.user_id AND u.is_active AND u.is_available AND u.deleted_at IS NULL").Joins("JOIN user_organizations uo ON uo.deleted_at IS NULL AND uo.user_id=u.id AND uo.organization_id=?", c.OrganizationID).Where("team_members.team_id=?", id).Find(&members).Error; err != nil {
					return nil, false, err
				}
				item.Status, item.Reason = "unassigned", "team_unavailable"
				for _, member := range members {
					full, err := inboxAgentFull(db, c, member.UserID)
					if err != nil {
						return nil, false, err
					}
					if !full {
						item.Status, item.Reason = "ready", "team_strategy_will_select_agent"
						break
					}
					item.Reason = "team_at_capacity"
				}
			case "tag":
				if !canAssign {
					item.Status, item.Reason = "blocked", "contact_edit_permission_required"
					break
				}
				var count int64
				if err := db.Model(&models.Tag{}).Where("organization_id=? AND name=?", c.OrganizationID, action.Value).Count(&count).Error; err != nil {
					return nil, false, err
				}
				if count == 0 {
					item.Status, item.Reason = "blocked", "tag_unavailable"
				}
			case "status":
				if c.Status == models.InboxCaseResolved {
					item.Status, item.Reason = "blocked", "case_resolved"
					break
				}
				if action.Value == string(models.InboxCaseResolved) {
					missing, err := a.missingInboxFields(db, c, user)
					if err != nil {
						return nil, false, err
					}
					if len(missing) > 0 {
						deferred := true
						for _, field := range missing {
							deferred = deferred && plannedFields[field.ID]
						}
						if deferred {
							item.Reason = "required_details_from_integration"
						} else {
							item.Status, item.Reason = "blocked", "required_details_missing"
							item.Detail = strconv.Itoa(len(missing))
						}
					}
				}
				if item.Status != "blocked" {
					c.Status = models.InboxCaseStatus(action.Value)
				}
			case "snooze":
				if c.Status == models.InboxCaseResolved {
					item.Status, item.Reason = "blocked", "case_resolved"
					break
				}
				c.Status = models.InboxCaseSnoozed
				item.Detail = strconv.Itoa(action.DurationMinutes)
			case "priority":
				if c.Status == models.InboxCaseResolved {
					item.Status, item.Reason = "blocked", "case_resolved"
					break
				}
				c.Priority = models.InboxCasePriority(action.Value)
			case "send_text", "send_template":
				item.Reason = "dispatch_revalidates_recipient_and_eligibility"
			}
		}
		if item.Status == "blocked" {
			eligible = false
		}
		out = append(out, item)
	}
	return out, eligible, nil
}

func inboxAgentFull(db *gorm.DB, c models.InboxCase, agent uuid.UUID) (bool, error) {
	if !inboxCountsForCapacity(c.Status) {
		return false, nil
	}
	var limit models.InboxAgentCapacity
	err := db.First(&limit, "organization_id=? AND account_id=? AND agent_id=?", c.OrganizationID, c.AccountID, agent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if limit.MaxActive == 0 {
		return false, nil
	}
	var count int64
	err = db.Model(&models.InboxCase{}).Where("organization_id=? AND account_id=? AND assignee_id=? AND id<>? AND status IN ?", c.OrganizationID, c.AccountID, agent, c.ID, inboxActiveStatuses).Count(&count).Error
	return count >= int64(limit.MaxActive), err
}
