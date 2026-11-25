package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const inboxCasePageSize = 100

var (
	errInboxConflict   = errors.New("conversation changed; refresh before editing")
	errInboxDisabled   = errors.New("inbox workflows are disabled")
	errInboxTransition = errors.New("invalid conversation transition")
)

type inboxThread struct {
	org, account, contact uuid.UUID
}

func (t inboxThread) query(db *gorm.DB) *gorm.DB {
	return db.Where("inbox_cases.organization_id = ? AND inbox_cases.account_id = ? AND inbox_cases.contact_id = ?", t.org, t.account, t.contact)
}

func (t inboxThread) latest(db *gorm.DB) (models.InboxCase, error) {
	var c models.InboxCase
	err := t.query(db).Order("opened_at DESC, id DESC").First(&c).Error
	return c, err
}

func (t inboxThread) lock(tx *gorm.DB) error {
	var contact models.Contact
	return tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("organization_id = ? AND id = ?", t.org, t.contact).First(&contact).Error
}

// Keep workflow transitions and inbound receipts serialized for each contact.
// Historical cases remain separate from the message stream.
func newInboxCase(t inboxThread, now time.Time, owner, team *uuid.UUID) models.InboxCase {
	return models.InboxCase{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: t.org, AccountID: t.account, ContactID: t.contact,
		AssigneeID: owner, TeamID: team, Status: models.InboxCaseOpen,
		Priority: models.InboxPriorityNormal, Version: 1, OpenedAt: now, StatusChangedAt: now,
	}
}

func appendInboxActivity(tx *gorm.DB, c models.InboxCase, event models.InboxCaseActivity) error {
	event.ID = uuid.New()
	event.OrganizationID = c.OrganizationID
	event.AccountID = c.AccountID
	event.CaseID = c.ID
	event.CaseVersion = c.Version
	switch {
	case event.Kind == "opened" && event.EventKey == nil, event.Kind == "reopened":
		event.WorkflowTrigger = "case_opened"
	case event.Kind == "opened" && strings.HasPrefix(*event.EventKey, "handoff:"):
		event.WorkflowTrigger = "case_opened"
	case event.Kind == "opened":
		event.WorkflowTrigger = "entry_message"
	case event.Kind == "incoming_message" && event.OccurredAt != nil, event.Kind == "customer_replied":
		event.WorkflowTrigger = "incoming_message"
	case event.Kind == "updated" && event.ActorID != nil && event.FromStatus != event.ToStatus:
		event.WorkflowTrigger = "status_changed"
	}
	event.AssigneeID, event.TeamID = c.AssigneeID, c.TeamID
	if err := tx.Create(&event).Error; err != nil {
		return err
	}
	return inboxquality.Sync(tx, &c, time.Now().UTC())
}

func inboxWorkflowEnabled(db *gorm.DB, orgID uuid.UUID, lock bool) (bool, error) {
	var org models.Organization
	q := db.Select("inbox_workflows_enabled").Where("id = ?", orgID)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := q.First(&org).Error
	return org.InboxWorkflowsEnabled, err
}

func (a *App) GetInboxWorkflowSettings(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, org, false)
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"enabled": enabled})
}

func (a *App) UpdateInboxWorkflowSettings(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if input.Enabled == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Enabled is required", nil, "")
	}
	if err := a.DB.Model(&models.Organization{}).Where("id = ?", org).
		Update("inbox_workflows_enabled", *input.Enabled).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"enabled": *input.Enabled})
}

// A legacy transfer to a restricted agent grants access only to the matching
// account here. A contact's explicit assignment keeps its existing scope.
func (a *App) scopeInboxCases(q *gorm.DB, user, org uuid.UUID) *gorm.DB {
	return scopeInboxCasesWithContactRead(q, user, org, a.HasPermission(user, models.ResourceContacts, models.ActionRead, org))
}

// The same query boundary can be used with freshly loaded permissions by
// background internal notifications, without trusting a Redis role cache.
func scopeInboxCasesWithContactRead(q *gorm.DB, user, org uuid.UUID, fullContactRead bool) *gorm.DB {
	q = q.Where("inbox_cases.organization_id = ?", org).
		Joins("JOIN contacts c ON c.id = inbox_cases.contact_id AND c.organization_id = inbox_cases.organization_id AND c.deleted_at IS NULL").
		Joins("JOIN whatsapp_accounts wa ON wa.id = inbox_cases.account_id AND wa.organization_id = inbox_cases.organization_id AND wa.deleted_at IS NULL")
	if fullContactRead {
		return q
	}
	return q.Where("inbox_cases.assignee_id = ? OR (inbox_cases.assignee_id IS NULL AND (c.assigned_user_id = ? OR EXISTS ("+
		"SELECT 1 FROM agent_transfers at WHERE at.contact_id = c.id AND at.organization_id = c.organization_id "+
		"AND at.whats_app_account = wa.name AND at.agent_id = ? AND at.status = ? AND at.deleted_at IS NULL)))",
		user, user, user, models.TransferStatusActive)
}

func (a *App) ListInboxCaseSummaries(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceChat, models.ActionRead)
	if err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, org, false)
	if err != nil {
		return a.inboxError(r, err)
	}
	cases := []models.InboxCase{}
	if !enabled {
		return r.SendEnvelope(map[string]any{"cases": cases})
	}
	values := strings.Split(string(r.RequestCtx.QueryArgs().Peek("contacts")), ",")
	if len(values) > inboxCasePageSize {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Too many contacts", nil, "")
	}
	contactIDs := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		id, parseErr := uuid.Parse(value)
		if parseErr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid contact ID", nil, "")
		}
		contactIDs = append(contactIDs, id)
	}
	err = a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).
		Where("inbox_cases.contact_id IN ?", contactIDs).
		Where("NOT EXISTS (SELECT 1 FROM inbox_cases newer WHERE newer.organization_id = inbox_cases.organization_id " +
			"AND newer.account_id = inbox_cases.account_id AND newer.contact_id = inbox_cases.contact_id AND newer.deleted_at IS NULL " +
			"AND (newer.opened_at, newer.id) > (inbox_cases.opened_at, inbox_cases.id))").
		Order("inbox_cases.contact_id, inbox_cases.account_id, inbox_cases.opened_at DESC, inbox_cases.id DESC").
		Find(&cases).Error
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"cases": cases})
}

func (a *App) inboxThreadAccess(db *gorm.DB, t inboxThread, user uuid.UUID) (models.Contact, error) {
	var contact models.Contact
	q := db.Where("contacts.organization_id = ? AND contacts.id = ?", t.org, t.contact).
		Joins("JOIN whatsapp_accounts wa ON wa.id = ? AND wa.organization_id = contacts.organization_id AND wa.deleted_at IS NULL", t.account)
	if !a.HasPermission(user, models.ResourceContacts, models.ActionRead, t.org) {
		q = q.Where("contacts.assigned_user_id = ? OR EXISTS (SELECT 1 FROM agent_transfers at "+
			"WHERE at.contact_id = contacts.id AND at.organization_id = contacts.organization_id "+
			"AND at.whats_app_account = wa.name AND at.agent_id = ? AND at.status = ? AND at.deleted_at IS NULL) OR EXISTS (?)",
			user, user, models.TransferStatusActive,
			latestInboxAssignment(db, t.org, user).Where("ic.account_id = wa.id AND ic.contact_id = contacts.id").Select("1"))
	}
	err := q.First(&contact).Error
	return contact, err
}

func (a *App) inboxRequest(r *fastglue.Request, action string) (inboxThread, uuid.UUID, error) {
	org, user, err := a.requireAuth(r, models.ResourceChat, action)
	if err != nil {
		return inboxThread{}, uuid.Nil, err
	}
	account, err := parsePathUUID(r, "account_id", "WhatsApp account")
	if err != nil {
		return inboxThread{}, uuid.Nil, err
	}
	contact, err := parsePathUUID(r, "id", "contact")
	return inboxThread{org, account, contact}, user, err
}

func (a *App) GetInboxConversation(r *fastglue.Request) error {
	t, user, err := a.inboxRequest(r, models.ActionRead)
	if err != nil {
		return nil
	}
	enabled, err := inboxWorkflowEnabled(a.DB, t.org, false)
	if err != nil {
		return a.inboxError(r, err)
	}
	if !enabled {
		return a.inboxError(r, errInboxDisabled)
	}
	if _, err = a.inboxThreadAccess(a.DB, t, user); err != nil {
		return a.inboxError(r, err)
	}
	c, err := t.latest(a.DB)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.SendEnvelope(map[string]any{"case": nil, "activity": []models.InboxCaseActivity{}})
	}
	if err != nil {
		return a.inboxError(r, err)
	}
	currentCaseID := c.ID
	var activities []struct {
		models.InboxCaseActivity
		ActorName    string `json:"actor_name"`
		AssigneeName string `json:"assignee_name"`
		TeamName     string `json:"team_name"`
	}
	scope := a.scopeInboxCaseHistory(t.query(a.DB.Model(&models.InboxCase{})), user, t.org).
		Select("inbox_cases.id")
	activityQuery := a.DB.Model(&models.InboxCaseActivity{}).Where("inbox_case_activities.organization_id = ? AND inbox_case_activities.account_id = ? AND case_id IN (?)", t.org, t.account, scope)
	if raw := string(r.RequestCtx.QueryArgs().Peek("case_id")); raw != "" {
		id, e := uuid.Parse(raw)
		if e != nil {
			return a.inboxError(r, errInboxTransition)
		}
		var selected models.InboxCase
		if e := a.scopeInboxCaseHistory(t.query(a.DB.Model(&models.InboxCase{})), user, t.org).First(&selected, "inbox_cases.id=?", id).Error; e != nil {
			return a.inboxError(r, e)
		}
		c = selected
		activityQuery = activityQuery.Where("case_id=?", id)
	}
	if cursor := string(r.RequestCtx.QueryArgs().Peek("activity_before")); cursor != "" {
		at, id, err := parseInboxActivityCursor(cursor)
		if err != nil {
			return a.inboxError(r, errInboxTransition)
		}
		activityQuery = activityQuery.Where("(inbox_case_activities.created_at,inbox_case_activities.id) < (?,?)", at, id)
	}
	if err := activityQuery.
		Joins("LEFT JOIN users actor ON actor.id=inbox_case_activities.actor_id AND actor.deleted_at IS NULL AND EXISTS(SELECT 1 FROM user_organizations uo WHERE uo.deleted_at IS NULL AND uo.user_id=actor.id AND uo.organization_id=inbox_case_activities.organization_id)").
		Joins("LEFT JOIN users owner ON owner.id=inbox_case_activities.assignee_id AND owner.deleted_at IS NULL AND EXISTS(SELECT 1 FROM user_organizations uo WHERE uo.deleted_at IS NULL AND uo.user_id=owner.id AND uo.organization_id=inbox_case_activities.organization_id)").
		Joins("LEFT JOIN teams team ON team.id=inbox_case_activities.team_id AND team.organization_id=inbox_case_activities.organization_id AND team.deleted_at IS NULL").
		Select("inbox_case_activities.*, COALESCE(actor.full_name,'') AS actor_name,COALESCE(owner.full_name,'') AS assignee_name,COALESCE(team.name,'') AS team_name").
		Where("kind <> ?", "incoming_message").Order("inbox_case_activities.created_at DESC, inbox_case_activities.id DESC").Limit(51).Scan(&activities).Error; err != nil {
		return a.inboxError(r, err)
	}
	if c.AssigneeID != nil && *c.AssigneeID != user && !a.HasPermission(user, models.ResourceContacts, models.ActionRead, t.org) {
		return a.inboxError(r, gorm.ErrRecordNotFound)
	}
	sla, err := a.inboxSLASnapshots(t.org, []models.InboxCase{c}, time.Now().UTC())
	if err != nil {
		return a.inboxError(r, err)
	}
	more := len(activities) > 50
	if more {
		activities = activities[:50]
	}
	cursor := ""
	if more {
		last := activities[len(activities)-1]
		cursor = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID.String()))
	}
	return r.SendEnvelope(map[string]any{"case": inboxSLACase{InboxCase: c, SLA: sla[c.ID]}, "activity": activities, "current_case_id": currentCaseID, "read_only": c.ID != currentCaseID, "activity_has_more": more, "activity_next_cursor": cursor})
}

func parseInboxActivityCursor(raw string) (time.Time, uuid.UUID, error) {
	if len(raw) > 256 {
		return time.Time{}, uuid.Nil, errInboxTransition
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, uuid.Nil, errInboxTransition
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, errInboxTransition
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	return at, id, err
}

type inboxCaseUpdate struct {
	CaseID       *uuid.UUID               `json:"case_id"`
	Version      int64                    `json:"version"`
	Status       models.InboxCaseStatus   `json:"status"`
	Priority     models.InboxCasePriority `json:"priority"`
	SnoozedUntil *time.Time               `json:"snoozed_until"`
}

func (input inboxCaseUpdate) valid(now time.Time) bool {
	switch input.Status {
	case "", models.InboxCaseOpen, models.InboxCasePending, models.InboxCaseWaiting, models.InboxCaseResolved:
		if input.SnoozedUntil != nil {
			return false
		}
	case models.InboxCaseSnoozed:
		if input.SnoozedUntil == nil || !input.SnoozedUntil.After(now) {
			return false
		}
	default:
		return false
	}
	switch input.Priority {
	case "", models.InboxPriorityLow, models.InboxPriorityNormal, models.InboxPriorityHigh, models.InboxPriorityUrgent:
		return input.Status != "" || input.Priority != ""
	}
	return false
}

func applyInboxTransition(tx *gorm.DB, c *models.InboxCase, input inboxCaseUpdate, actor *uuid.UUID, now time.Time, kind string) error {
	event := models.InboxCaseActivity{ActorID: actor, OccurredAt: &now, Kind: kind, FromStatus: c.Status, FromPriority: c.Priority}
	if input.Status != "" {
		if input.Status == models.InboxCaseResolved && c.Status != models.InboxCaseResolved {
			proposed := *c
			if input.Priority != "" {
				proposed.Priority = input.Priority
			}
			if err := checkInboxRequiredFields(tx, proposed); err != nil {
				return err
			}
		}
		if input.Status != c.Status {
			c.StatusChangedAt = now
		}
		c.Status = input.Status
		if !inboxCountsForCapacity(event.FromStatus) && inboxCountsForCapacity(c.Status) {
			if err := protectInboxActiveAssignment(tx, c); err != nil {
				return err
			}
		}
		c.SnoozedUntil = input.SnoozedUntil
		if c.Status == models.InboxCaseResolved {
			c.ResolvedAt = &now
		}
	}
	if input.Priority != "" {
		c.Priority = input.Priority
	}
	c.Version++
	if err := tx.Save(c).Error; err != nil {
		return err
	}
	event.ToStatus, event.ToPriority = c.Status, c.Priority
	return appendInboxActivity(tx, *c, event)
}

func (a *App) UpdateInboxConversation(r *fastglue.Request) error {
	t, user, err := a.inboxRequest(r, models.ActionWrite)
	if err != nil {
		return nil
	}
	var input inboxCaseUpdate
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	now := time.Now().UTC()
	if !input.valid(now) {
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
		contact, err := a.inboxThreadAccess(tx, t, user)
		if err != nil {
			return err
		}
		c, err = t.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if (input.CaseID == nil && c.ID != uuid.Nil) ||
			(input.CaseID != nil && (*input.CaseID != c.ID || input.Version != c.Version)) {
			return errInboxConflict
		}
		if c.AssigneeID != nil && *c.AssigneeID != user && !a.HasPermission(user, models.ResourceContacts, models.ActionRead, t.org) {
			return gorm.ErrRecordNotFound
		}
		if c.ID != uuid.Nil && c.Status != models.InboxCaseResolved {
			return applyInboxTransition(tx, &c, input, &user, now, "updated")
		}
		if input.Status != models.InboxCaseOpen {
			return errInboxTransition
		}
		kind := "opened"
		if c.ID != uuid.Nil {
			kind = "reopened"
		}
		owner, team := contact.AssignedUserID, c.TeamID
		if c.AssigneeID != nil {
			owner = c.AssigneeID
		}
		c = newInboxCase(t, now, owner, team)
		if err := protectInboxActiveAssignment(tx, &c); err != nil {
			return err
		}
		if input.Priority != "" {
			c.Priority = input.Priority
		}
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		if err := inboxquality.AssignPolicy(tx, &c, false); err != nil {
			return err
		}
		return appendInboxActivity(tx, c, models.InboxCaseActivity{ActorID: &user, Kind: kind, ToStatus: c.Status, ToPriority: c.Priority})
	})
	if errors.Is(err, errRequiredInboxFields) {
		if e := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, t.org).First(&models.InboxCase{}, "inbox_cases.id=?", c.ID).Error; e != nil {
			return a.inboxError(r, e)
		}
		missing, e := a.missingInboxFields(a.DB, c, user)
		if e != nil {
			return a.inboxError(r, e)
		}
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, errRequiredInboxFields.Error(), map[string]any{"missing_fields": missing}, "required_inbox_fields")
	}

	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"case": c})
}

func (a *App) inboxError(r *fastglue.Request, err error) error {
	switch {
	case errors.Is(err, errInboxFieldRevision):
		return r.SendErrorEnvelope(fasthttp.StatusConflict, errInboxFieldRevision.Error(), nil, "field_revision_conflict")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Conversation not found", nil, "")
	case errors.Is(err, errInboxDisabled):
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, errInboxDisabled.Error(), nil, "")
	case errors.Is(err, errInboxConflict):
		return r.SendErrorEnvelope(fasthttp.StatusConflict, errInboxConflict.Error(), nil, "")
	case errors.Is(err, errInboxSendIneligible):
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, errInboxSendIneligible.Error(), nil, "inbox_send_ineligible")
	case errors.Is(err, errInboxAssignmentRate):
		return r.SendErrorEnvelope(fasthttp.StatusConflict, errInboxAssignmentRate.Error(), nil, "assignment_rate_limited")
	case errors.Is(err, errInboxRoutingPermission):
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, errInboxRoutingPermission.Error(), nil, "assignment_permission_required")
	case errors.Is(err, errInboxOverrideForbidden):
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, errInboxOverrideForbidden.Error(), nil, "assignment_override_forbidden")
	case errors.Is(err, errInboxRoutingConfig):
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, errInboxRoutingConfig.Error(), nil, "routing_configuration_invalid")
	case errors.Is(err, errRequiredInboxFields):
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, errRequiredInboxFields.Error(), nil, "required_inbox_fields")
	case errors.Is(err, errInboxTransition):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errInboxTransition.Error(), nil, "")
	default:
		a.Log.Error("Inbox operation failed", "code", audit.ErrorCode(err))
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Inbox operation failed", nil, "")
	}
}

func (a *App) recordInboxInbound(account *models.WhatsAppAccount, contact *models.Contact, eventKey string, received time.Time, transfer *models.AgentTransfer) {
	a.recordInboxEntry(account, contact, eventKey, received, received, transfer)
}

func (a *App) recordInboxEntry(account *models.WhatsAppAccount, contact *models.Contact, eventKey string, received, entered time.Time, transfer *models.AgentTransfer) {
	if eventKey == "" {
		return
	}
	t := inboxThread{account.OrganizationID, account.ID, contact.ID}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, t.org, true)
		if err != nil || !enabled {
			return err
		}
		if err := t.lock(tx); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.InboxCaseActivity{}).Where("organization_id = ? AND account_id = ? AND event_key = ?",
			t.org, t.account, eventKey).Count(&count).Error; err != nil || count != 0 {
			return err
		}
		c, err := t.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		kind := "incoming_message"
		beforeIntake := c
		from := c.Status
		if c.ID == uuid.Nil || c.Status == models.InboxCaseResolved {
			// A previously unseen late delivery still belongs to the case
			// that was current when the message entered the inbox.
			if c.ResolvedAt != nil && !entered.After(*c.ResolvedAt) {
				return appendInboxActivity(tx, c, models.InboxCaseActivity{EventKey: &eventKey, Kind: kind})
			}
			owner := contact.AssignedUserID
			var team *uuid.UUID
			if transfer != nil {
				team = transfer.TeamID
				if transfer.AgentID != nil {
					owner = transfer.AgentID
				}
			}
			c = newInboxCase(t, entered, owner, team)
			if err := protectInboxActiveAssignment(tx, &c); err != nil {
				return err
			}
			if c.AssigneeID == nil && c.TeamID != nil && c.RoutingReason == "" {
				c.RoutingReason = "available_search"
			}
			c.LastInboundAt = &received
			kind = "opened"
			if err := tx.Create(&c).Error; err != nil {
				return err
			}
		} else {
			if c.LastInboundAt == nil || received.After(*c.LastInboundAt) {
				c.LastInboundAt = &received
			}
			if !received.Before(c.StatusChangedAt) && (c.Status == models.InboxCaseWaiting || c.Status == models.InboxCaseSnoozed) {
				c.Status, c.StatusChangedAt, c.SnoozedUntil = models.InboxCaseOpen, received, nil
				if err := protectInboxActiveAssignment(tx, &c); err != nil {
					return err
				}
				kind = "customer_replied"
			}
			if c.LastHumanReplyAt != nil && received.After(*c.LastHumanReplyAt) && (c.NextReplyFrom == nil || received.Before(*c.NextReplyFrom)) {
				c.NextReplyFrom = &received
			}
			c.Version++
			if err := tx.Save(&c).Error; err != nil {
				return err
			}
		}
		if transfer != nil && transfer.Source == models.TransferSourceFlow && transfer.InboxIntakeSessionID != nil && strings.HasPrefix(eventKey, "handoff:") {
			if err := a.applyStagedInboxIntake(tx, c, beforeIntake, *transfer.InboxIntakeSessionID, entered); err != nil {
				return err
			}
		}
		return appendInboxActivity(tx, c, models.InboxCaseActivity{EventKey: &eventKey, OccurredAt: &received, Kind: kind, FromStatus: from, ToStatus: c.Status})
	})
	if err != nil {
		a.Log.Error("Could not record inbox activity", "contact_id", contact.ID, "account_id", account.ID, "error", err)
	}
}

func (a *App) recordInboxHandoff(account *models.WhatsAppAccount, contact *models.Contact, transfer *models.AgentTransfer) {
	enabled, err := inboxWorkflowEnabled(a.DB, account.OrganizationID, false)
	if err != nil || !enabled {
		return
	}
	var msg models.Message
	err = a.DB.Select("whats_app_message_id", "created_at").
		Where("organization_id = ? AND contact_id = ? AND whats_app_account = ? AND direction = ? AND whats_app_message_id <> ''",
			account.OrganizationID, contact.ID, account.Name, models.DirectionIncoming).
		Order("created_at DESC, id DESC").First(&msg).Error
	if err == nil {
		a.recordInboxEntry(account, contact, "handoff:"+transfer.ID.String(), msg.CreatedAt, transfer.TransferredAt, transfer)
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		a.recordInboxInbound(account, contact, "handoff:"+transfer.ID.String(), transfer.TransferredAt, transfer)
	}
}

func (a *App) RunInboxSnoozes(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := a.reopenDueInboxCases(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			a.Log.Error("Inbox snooze processing failed", "error", err)
		}
		if err := expireInboxIntake(a.DB.WithContext(ctx), time.Now().UTC()); err != nil && ctx.Err() == nil {
			a.Log.Error("Intake expiry failed", "code", audit.ErrorCode(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) reopenDueInboxCases(ctx context.Context, now time.Time) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cases []models.InboxCase
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND snoozed_until <= ? AND EXISTS (SELECT 1 FROM organizations o "+
				"WHERE o.id = inbox_cases.organization_id AND o.inbox_workflows_enabled AND o.deleted_at IS NULL)", models.InboxCaseSnoozed, now).
			Order("snoozed_until, id").Limit(inboxCasePageSize).Find(&cases).Error
		if err != nil {
			return err
		}
		for i := range cases {
			if err := applyInboxTransition(tx, &cases[i], inboxCaseUpdate{Status: models.InboxCaseOpen}, nil, now, "snooze_elapsed"); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *App) trackInboxIncoming(account *models.WhatsAppAccount, contact *models.Contact, message *models.Message) {
	enabled, err := inboxWorkflowEnabled(a.DB, account.OrganizationID, false)
	if err != nil || !enabled {
		return
	}
	var transfer models.AgentTransfer
	err = a.DB.Where("organization_id = ? AND contact_id = ? AND whats_app_account = ? AND status = ?",
		account.OrganizationID, contact.ID, account.Name, models.TransferStatusActive).
		Order("transferred_at DESC").First(&transfer).Error
	if err == nil {
		a.recordInboxInbound(account, contact, message.WhatsAppMessageID, message.CreatedAt, &transfer)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		thread := inboxThread{account.OrganizationID, account.ID, contact.ID}
		if _, lookupErr := thread.latest(a.DB); lookupErr == nil {
			a.recordInboxInbound(account, contact, message.WhatsAppMessageID, message.CreatedAt, nil)
		}
	}
}

func (a *App) recordInboxHumanReply(account *models.WhatsAppAccount, message *models.Message, sequenced ...bool) {
	enabled, err := inboxWorkflowEnabled(a.DB, account.OrganizationID, false)
	if err != nil || !enabled || message.SentByUserID == nil {
		return
	}
	t := inboxThread{account.OrganizationID, account.ID, message.ContactID}
	at := time.Now().UTC()
	eventKey := "reply:" + message.ID.String()
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := t.lock(tx); err != nil {
			return err
		}
		c, err := t.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil || c.Status == models.InboxCaseResolved || message.CreatedAt.Before(c.OpenedAt) {
			return err
		}
		var recorded int64
		if err := tx.Model(&models.InboxCaseActivity{}).
			Where("organization_id = ? AND account_id = ? AND event_key = ?", t.org, t.account, eventKey).
			Count(&recorded).Error; err != nil || recorded != 0 {
			return err
		}
		beforeDigest := ""
		if len(sequenced) > 0 && sequenced[0] {
			beforeDigest, err = inboxSendCaseDigest(tx, c)
			if err != nil {
				return err
			}
		}
		if c.FirstHumanReplyAt == nil {
			c.FirstHumanReplyAt = &at
		}
		c.LastHumanReplyAt = &at
		if c.NextReplyFrom != nil && !c.NextReplyFrom.After(at) {
			c.NextReplyFrom = nil
		}
		c.Version++
		if err := tx.Save(&c).Error; err != nil {
			return err
		}
		if err := appendInboxActivity(tx, c, models.InboxCaseActivity{Kind: "human_reply", EventKey: &eventKey, ActorID: message.SentByUserID}); err != nil {
			return err
		}
		if beforeDigest != "" {
			afterDigest, err := inboxSendCaseDigest(tx, c)
			if err != nil {
				return err
			}
			// Advance only the digest representing this send's own change. A
			// concurrent unrelated edit must still pause downstream actions.
			return tx.Model(&models.InboxWorkflowRun{}).Where("id=? AND organization_id=? AND case_id=? AND sequenced AND state=? AND case_digest=?", message.ID, t.org, c.ID, inboxSequenceSending, beforeDigest).Update("case_digest", afterDigest).Error
		}
		return nil
	})
	if err != nil {
		a.Log.Error("Could not record agent reply in inbox", "message_id", message.ID, "error", err)
	}
}
