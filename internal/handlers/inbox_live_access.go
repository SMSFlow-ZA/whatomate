package handlers

import (
	"context"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Socket identity supplies org/user/claims; the watch cannot supply authority.
// Redis is used only to check a bound session, never as the role authority.
func (a *App) inboxLiveViewer(ctx context.Context, org, user uuid.UUID, claims *middleware.JWTClaims) (auditViewer, error) {
	db := a.DB.WithContext(ctx)
	viewer, err := loadAuditViewer(db, org, user, claims)
	if err != nil {
		return viewer, err
	}
	if claims != nil && claims.SessionID != "" {
		if err := middleware.ValidateSession(ctx, a.Redis, claims); err != nil {
			return viewer, errAuditUnauthenticated
		}
	}
	enabled, err := inboxWorkflowEnabled(db, org, false)
	if err != nil {
		return viewer, err
	}
	if !enabled {
		return viewer, errInboxDisabled
	}
	return viewer, nil
}

func inboxLiveOwnedScope(db *gorm.DB, org uuid.UUID, scope inboxlive.WatchScope) (bool, error) {
	for _, target := range []struct {
		id    *uuid.UUID
		model any
	}{{scope.AccountID, &models.WhatsAppAccount{}}, {scope.TeamID, &models.Team{}}} {
		if target.id == nil {
			continue
		}
		var count int64
		if err := db.Model(target.model).Where("id=? AND organization_id=?", *target.id, org).Count(&count).Error; err != nil {
			return false, err
		}
		if count != 1 {
			return false, nil
		}
	}
	return true, nil
}

func inboxLiveConversationAccess(db *gorm.DB, viewer auditViewer, account, contact uuid.UUID) (bool, error) {
	q := db.Model(&models.Contact{}).
		Joins("JOIN whatsapp_accounts wa ON wa.id=? AND wa.organization_id=contacts.organization_id AND wa.deleted_at IS NULL", account).
		Where("contacts.organization_id=? AND contacts.id=?", viewer.Org, contact)
	if !viewer.has(models.ResourceContacts, models.ActionRead) {
		grant := latestInboxAssignment(db.Session(&gorm.Session{NewDB: true}), viewer.Org, viewer.User).
			Where("ic.account_id=wa.id AND ic.contact_id=contacts.id").Select("1")
		blocked := latestInboxCases(db.Session(&gorm.Session{NewDB: true}), viewer.Org).
			Where("ic.account_id=wa.id AND ic.contact_id=contacts.id AND ic.assignee_id IS NOT NULL AND ic.assignee_id<>?", viewer.User).Select("1")
		transfer := db.Session(&gorm.Session{NewDB: true}).Model(&models.AgentTransfer{}).
			Where("organization_id=? AND contact_id=contacts.id AND whats_app_account=wa.name AND agent_id=? AND status=?", viewer.Org, viewer.User, models.TransferStatusActive).
			Where("NOT EXISTS (?)", blocked).Select("1")
		// Existing explicit contact assignment remains a contact-level privilege.
		// Case/transfer grants are account-scoped; a former transfer cannot defeat
		// the current case assignment in that account.
		q = q.Where("contacts.assigned_user_id=? OR EXISTS (?) OR EXISTS (?)", viewer.User, grant, transfer)
	}
	var count int64
	err := q.Count(&count).Error
	return count == 1, err
}

// This gate is repeated for subscription and delivery. It never borrows a
// client role flag or expands the existing HTTP read/workload boundaries.
func inboxLiveScopeAllowed(db *gorm.DB, viewer auditViewer, scope inboxlive.WatchScope) (bool, error) {
	if scope.Validate() != nil {
		return false, nil
	}
	allowed, err := inboxLiveOwnedScope(db, viewer.Org, scope)
	if err != nil || !allowed {
		return false, err
	}
	switch scope.Kind {
	case "case":
		if !viewer.has(models.ResourceChat, models.ActionRead) {
			return false, nil
		}
		q := scopeInboxCaseHistoryWithContactRead(db.Model(&models.InboxCase{}), viewer.User, viewer.Org, viewer.has(models.ResourceContacts, models.ActionRead)).
			Where("inbox_cases.id=?", *scope.CaseID)
		var count int64
		err := q.Count(&count).Error
		return count == 1, err
	case "conversation":
		if !viewer.has(models.ResourceChat, models.ActionRead) {
			return false, nil
		}
		return inboxLiveConversationAccess(db, viewer, *scope.AccountID, *scope.ContactID)
	case "contact":
		if !viewer.has(models.ResourceContacts, models.ActionRead) {
			return false, nil
		}
		var count int64
		err := db.Model(&models.Contact{}).Where("id=? AND organization_id=?", *scope.ContactID, viewer.Org).Count(&count).Error
		return count == 1, err
	case "capacity":
		return viewer.has(models.ResourceTeams, models.ActionRead), nil
	case "workload", "supervisor":
		return viewer.has(models.ResourceAnalyticsAgents, models.ActionRead), nil
	case "queue":
		// An empty queue may watch for its first permitted assignment. Every
		// resource hint still requires a separate current visibility check.
		return viewer.has(models.ResourceChat, models.ActionRead), nil
	default:
		return false, nil
	}
}

func (a *App) inboxLiveAuthorizer(org, user uuid.UUID, claims *middleware.JWTClaims) inboxlive.Authorizer {
	return func(ctx context.Context, scope inboxlive.WatchScope, event *inboxlive.Event) (inboxlive.Decision, error) {
		viewer, err := a.inboxLiveViewer(ctx, org, user, claims)
		if err != nil {
			return inboxlive.Decision{}, err
		}
		db := a.DB.WithContext(ctx)
		allowed, err := inboxLiveScopeAllowed(db, viewer, scope)
		decision := inboxlive.Decision{Allowed: allowed, Authority: viewer.fingerprint()}
		if err != nil || !allowed || event == nil {
			return decision, err
		}
		if event.OrganizationID != org {
			return decision, nil
		}
		if event.Kind == "access" || event.Kind == "configuration" {
			// No resource-specific information is exposed for configuration or
			// security changes. Re-fetch only this already-authorised context.
			decision.Match, decision.Reset = true, true
			return decision, nil
		}
		decision.Match, decision.Reset, err = inboxLiveEventVisible(db, viewer, scope, event)
		return decision, err
	}
}

func inboxLiveScopeMatches(scope inboxlive.WatchScope, hint *inboxlive.Scope) bool {
	if hint == nil {
		return false
	}
	for _, pair := range [][2]*uuid.UUID{{scope.AccountID, hint.AccountID}, {scope.TeamID, hint.TeamID}, {scope.ContactID, hint.ContactID}} {
		if pair[0] != nil && (pair[1] == nil || *pair[0] != *pair[1]) {
			return false
		}
	}
	return true
}

func inboxLiveTeamMember(db *gorm.DB, org, team, user uuid.UUID) (bool, error) {
	var count int64
	err := db.Model(&models.TeamMember{}).Joins("JOIN teams t ON t.id=team_members.team_id AND t.organization_id=? AND t.deleted_at IS NULL", org).
		Where("team_members.team_id=? AND team_members.user_id=?", team, user).Count(&count).Error
	return count > 0, err
}

func inboxLiveWorkloadMatches(db *gorm.DB, viewer auditViewer, scope inboxlive.WatchScope, hint *inboxlive.Scope) (bool, error) {
	if hint == nil || (scope.AccountID != nil && (hint.AccountID == nil || *scope.AccountID != *hint.AccountID)) {
		return false, nil
	}
	if scope.TeamID != nil {
		if hint.AssigneeID != nil {
			member, err := inboxLiveTeamMember(db, viewer.Org, *scope.TeamID, *hint.AssigneeID)
			if err != nil || !member {
				return false, err
			}
		} else if hint.TeamID == nil || *hint.TeamID != *scope.TeamID {
			return false, nil
		}
	}
	if !viewer.has(models.ResourceTeams, models.ActionRead) {
		if hint.AssigneeID != nil {
			return *hint.AssigneeID == viewer.User, nil
		}
		if hint.TeamID == nil {
			return false, nil
		}
		return inboxLiveTeamMember(db, viewer.Org, *hint.TeamID, viewer.User)
	}
	return true, nil
}

func inboxLiveEventVisible(db *gorm.DB, viewer auditViewer, scope inboxlive.WatchScope, event *inboxlive.Event) (bool, bool, error) {
	full := viewer.has(models.ResourceContacts, models.ActionRead)
	if scope.Kind == "capacity" && event.Kind == "case" {
		// Team-read permits aggregate capacity across the agent's teams, but
		// never grants that viewer access to the underlying case identity.
		var current models.InboxCase
		err := db.Select("account_id,assignee_id").Where("id=? AND organization_id=?", event.ResourceID, viewer.Org).Limit(1).Find(&current).Error
		if err != nil {
			return false, false, err
		}
		hints := []*inboxlive.Scope{event.Before, event.After}
		if current.AccountID != uuid.Nil {
			hints = append(hints, &inboxlive.Scope{AccountID: &current.AccountID, AssigneeID: current.AssigneeID})
		}
		for _, hint := range hints {
			matched, err := inboxLiveWorkloadMatches(db, viewer, scope, hint)
			if err != nil {
				return false, false, err
			}
			if matched && hint.AssigneeID != nil {
				return true, true, nil
			}
		}
		return false, false, nil
	}
	if event.Kind == "workload" {
		if scope.Kind != "capacity" && scope.Kind != "workload" && scope.Kind != "supervisor" {
			return false, false, nil
		}
		for _, hint := range []*inboxlive.Scope{event.Before, event.After} {
			matched, err := inboxLiveWorkloadMatches(db, viewer, scope, hint)
			if err != nil {
				return false, false, err
			}
			if !matched {
				continue
			}
			if !full && scope.Kind != "capacity" {
				if hint.AccountID == nil {
					continue // Unknown account cannot expand restricted workload scope.
				}
				var count int64
				err := scopeInboxCasesWithContactRead(db.Model(&models.InboxCase{}), viewer.User, viewer.Org, false).
					Where("inbox_cases.account_id=?", *hint.AccountID).Count(&count).Error
				if err != nil {
					return false, false, err
				}
				if count == 0 {
					continue
				}
			}
			return true, true, nil
		}
		return false, false, nil
	}
	if event.Kind != "case" && event.Kind != "contact" {
		return false, false, nil
	}
	if scope.Kind == "capacity" {
		return false, false, nil
	}
	if scope.Kind == "contact" {
		return event.Kind == "contact" && event.ResourceID == *scope.ContactID, false, nil
	}
	q := scopeInboxCaseHistoryWithContactRead(db.Model(&models.InboxCase{}), viewer.User, viewer.Org, full)
	if event.Kind == "case" {
		q = q.Where("inbox_cases.id=?", event.ResourceID)
	} else {
		q = q.Where("inbox_cases.contact_id=?", event.ResourceID)
	}
	if scope.CaseID != nil {
		q = q.Where("inbox_cases.id=?", *scope.CaseID)
	}
	if scope.AccountID != nil {
		q = q.Where("inbox_cases.account_id=?", *scope.AccountID)
	}
	if scope.TeamID != nil {
		if scope.Kind == "workload" {
			q = q.Where("EXISTS (SELECT 1 FROM team_members lm JOIN teams lt ON lt.id=lm.team_id AND lt.organization_id=inbox_cases.organization_id AND lt.deleted_at IS NULL WHERE lm.team_id=? AND lm.user_id=inbox_cases.assignee_id AND lm.deleted_at IS NULL)", *scope.TeamID)
		} else {
			q = q.Where("inbox_cases.team_id=?", *scope.TeamID)
		}
	}
	if scope.ContactID != nil {
		q = q.Where("inbox_cases.contact_id=?", *scope.ContactID)
	}
	if scope.Kind == "workload" && !viewer.has(models.ResourceTeams, models.ActionRead) {
		q = q.Where("inbox_cases.assignee_id=?", viewer.User)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, false, err
	}
	if count > 0 {
		return true, false, nil
	}
	if event.Kind == "contact" && scope.CaseID == nil &&
		(scope.ContactID == nil || *scope.ContactID == event.ResourceID) &&
		(full || (event.Before != nil && event.Before.AssigneeID != nil && *event.Before.AssigneeID == viewer.User)) {
		// Contact ownership is an existing contact-level grant, spanning owned
		// accounts. Clear stale rows after its removal without identifying which
		// previously visible case moved out of scope.
		return true, true, nil
	}
	// A previously visible assignment moving out of a queue needs a generic
	// reset. Never send its opaque resource/version or its new destination.
	beforeMatches := inboxLiveScopeMatches(scope, event.Before)
	if scope.Kind == "workload" {
		var err error
		beforeMatches, err = inboxLiveWorkloadMatches(db, viewer, scope, event.Before)
		if err != nil {
			return false, false, err
		}
	}
	if event.Kind == "case" && scope.CaseID == nil && beforeMatches &&
		(full || (event.Before.AssigneeID != nil && *event.Before.AssigneeID == viewer.User)) {
		if scope.Kind != "workload" || viewer.has(models.ResourceTeams, models.ActionRead) || (event.Before.AssigneeID != nil && *event.Before.AssigneeID == viewer.User) {
			return true, true, nil
		}
	}
	return false, false, nil
}
