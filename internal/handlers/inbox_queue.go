package handlers

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxQueueRow struct {
	models.InboxCase
	AccountName    string            `json:"account_name"`
	SLA            *inboxSLASnapshot `gorm:"-" json:"sla,omitempty"`
	ContactName    string            `json:"contact_name"`
	MessagePreview string            `json:"message_preview"`
	AssigneeName   string            `json:"assignee_name"`
	TeamName       string            `json:"team_name"`
}

func validateInboxFilter(f models.InboxQueueFilter) bool {
	switch f.Queue {
	case "", "mine", "unassigned", "awaiting_reply", "follow_ups":
	default:
		return false
	}
	switch f.Status {
	case "", models.InboxCaseOpen, models.InboxCasePending, models.InboxCaseWaiting, models.InboxCaseSnoozed, models.InboxCaseResolved:
	default:
		return false
	}
	if f.Priority != "" && !(inboxCaseUpdate{Priority: f.Priority}).valid(time.Time{}) {
		return false
	}
	if len(f.Fields)+len(f.Predicates) > 20 || len(f.Tags) > 20 || (f.Match != "" && f.Match != "all" && f.Match != "any") || len(f.MessageContains) > 500 {
		return false
	}
	for _, tag := range f.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 100 {
			return false
		}
	}
	return true
}

func (a *App) inboxFeatureAuth(r *fastglue.Request, action string) (uuid.UUID, uuid.UUID, bool) {
	org, user, err := a.requireAuth(r, models.ResourceChat, action)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	enabled, err := inboxWorkflowEnabled(a.DB, org, false)
	if err != nil {
		_ = a.inboxError(r, err)
		return uuid.Nil, uuid.Nil, false
	}
	if !enabled {
		_ = a.inboxError(r, errInboxDisabled)
	}
	return org, user, enabled
}

func (a *App) ListInboxQueue(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	args := r.RequestCtx.QueryArgs()
	var f models.InboxQueueFilter
	if raw := args.Peek("filters"); len(raw) > 0 {
		if err := json.Unmarshal(raw, &f); err != nil || !validateInboxFilter(f) {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid inbox filters", nil, "")
		}
	}
	if raw := string(args.Peek("view_id")); raw != "" {
		id, e := uuid.Parse(raw)
		if e != nil {
			return a.inboxError(r, errInboxTransition)
		}
		var view models.InboxSavedView
		if err := a.accessibleInboxViews(a.DB, org, user).First(&view, "id=?", id).Error; err != nil {
			return a.inboxError(r, err)
		}
		if view.TeamID != nil {
			f.TeamID = view.TeamID
		}
	}
	if f.MessageContains != "" {
		return a.inboxError(r, errInboxTransition)
	}
	pg := parseInboxQueuePage(args)
	baseFilter := f
	baseFilter.Queue = ""
	base, err := a.inboxQueueQuery(org, user, baseFilter)
	if err != nil {
		return a.inboxError(r, err)
	}
	q := applyInboxQueueKind(base.Session(&gorm.Session{}), f.Queue, user)
	rows := []inboxQueueRow{}
	err = q.Select(inboxQueueColumns).Joins(inboxQueueOwnerJoin).Joins(inboxQueueTeamJoin).
		Order("CASE inbox_cases.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, inbox_cases.opened_at, inbox_cases.id").
		Offset(pg).Limit(51).Scan(&rows).Error
	if err != nil {
		return a.inboxError(r, err)
	}
	rows, hasMore, err := a.prepareInboxRows(org, rows)
	if err != nil {
		return a.inboxError(r, err)
	}
	response := map[string]any{"cases": rows, "has_more": hasMore}
	if string(args.Peek("include_counts")) == "true" {
		counts := map[string]int64{}
		for _, queue := range []string{"", "mine", "unassigned", "awaiting_reply", "follow_ups"} {
			scoped := applyInboxQueueKind(base.Session(&gorm.Session{}), queue, user)
			var count int64
			if err := scoped.Count(&count).Error; err != nil {
				return a.inboxError(r, err)
			}
			key := queue
			if key == "" {
				key = "all"
			}
			counts[key] = count
		}
		response["counts"] = counts
	}
	return r.SendEnvelope(response)
}

func (a *App) prepareInboxRows(org uuid.UUID, rows []inboxQueueRow) ([]inboxQueueRow, bool, error) {
	// Match the contact service's phone masking rules for numeric profile names.
	if a.ShouldMaskPhoneNumbers(org) {
		for i := range rows {
			rows[i].ContactName = utils.MaskIfPhoneNumber(rows[i].ContactName)
		}
	}
	hasMore := len(rows) > 50
	if hasMore {
		rows = rows[:50]
	}
	base := make([]models.InboxCase, len(rows))
	for i := range rows {
		base[i] = rows[i].InboxCase
	}
	snapshots, err := a.inboxSLASnapshots(org, base, time.Now().UTC())
	if err != nil {
		return nil, false, err
	}
	for i := range rows {
		rows[i].SLA = snapshots[rows[i].ID]
	}
	return rows, hasMore, nil
}

func parseInboxQueuePage(args *fasthttp.Args) int {
	offset, _ := strconv.Atoi(string(args.Peek("offset")))
	if offset < 0 {
		return 0
	}
	return offset
}

func (a *App) ListInboxSavedViews(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	views := []models.InboxSavedView{}
	q := a.accessibleInboxViews(a.DB, org, user)
	if err := q.Order("name, id").Find(&views).Error; err != nil {
		return a.inboxError(r, err)
	}
	result := make([]map[string]any, 0, len(views))
	for _, view := range views {
		editable := view.TeamID == nil && view.OwnerID == user
		if view.TeamID != nil {
			permitted, err := a.canPublishInboxView(org, user, *view.TeamID)
			if err != nil {
				return a.inboxError(r, err)
			}
			editable = permitted
		}
		result = append(result, map[string]any{"id": view.ID, "owner_id": view.OwnerID, "team_id": view.TeamID, "name": view.Name, "filters": view.Filters, "capabilities": map[string]bool{"edit": editable, "remove": editable, "share": view.OwnerID == user && a.HasPermission(user, models.ResourceTeams, models.ActionWrite, org)}})
	}
	publication := a.DB.Model(&models.Team{}).Where("teams.organization_id=? AND teams.is_active", org)
	if !a.HasPermission(user, models.ResourceTeams, models.ActionWrite, org) {
		publication = publication.Where("EXISTS (SELECT 1 FROM team_members tm WHERE tm.team_id=teams.id AND tm.user_id=? AND tm.role=? AND tm.deleted_at IS NULL)", user, models.TeamRoleManager)
	}
	teams := []struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}{}
	if err := publication.Select("id,name").Order("name,id").Scan(&teams).Error; err != nil {
		return a.inboxError(r, err)
	}
	for _, view := range result {
		capabilities := view["capabilities"].(map[string]bool)
		capabilities["share"] = view["owner_id"] == user && len(teams) > 0
	}
	return r.SendEnvelope(map[string]any{"views": result, "publish_teams": teams})
}

func (a *App) UpdateInboxSavedView(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "saved view")
	if err != nil {
		return nil
	}
	var input struct {
		Name    string                  `json:"name"`
		TeamID  *uuid.UUID              `json:"team_id"`
		Filters models.InboxQueueFilter `json:"filters"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 || !validateInboxFilter(input.Filters) || input.Filters.MessageContains != "" {
		return a.inboxError(r, errInboxTransition)
	}
	allowContact := a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)
	if err := validateInboxFieldMatchers(a.DB, org, input.Filters.Fields, allowContact); err != nil {
		return a.inboxError(r, err)
	}
	if err := validateInboxPredicates(a.DB, org, input.Filters, allowContact); err != nil {
		return a.inboxError(r, err)
	}
	var view models.InboxSavedView
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&view, "organization_id=? AND id=?", org, id).Error; err != nil {
			return err
		}
		if view.TeamID != nil {
			permitted, err := a.canPublishInboxView(org, user, *view.TeamID)
			if err != nil {
				return err
			}
			if !permitted {
				return gorm.ErrRecordNotFound
			}
		} else if view.OwnerID != user {
			return gorm.ErrRecordNotFound
		}
		if input.TeamID != nil {
			permitted, err := a.canPublishInboxView(org, user, *input.TeamID)
			if err != nil {
				return err
			}
			if !permitted {
				return errInboxTransition
			}
			input.Filters.TeamID = input.TeamID
		}
		view.Name, view.TeamID, view.Filters = input.Name, input.TeamID, input.Filters
		return tx.Save(&view).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"view": view})
}

func (a *App) canPublishInboxView(org, user, teamID uuid.UUID) (bool, error) {
	var count int64
	q := a.DB.Model(&models.Team{}).Where("teams.organization_id = ? AND teams.id = ? AND teams.is_active", org, teamID)
	if !a.HasPermission(user, models.ResourceTeams, models.ActionWrite, org) {
		q = q.Joins("JOIN team_members tm ON tm.team_id = teams.id AND tm.deleted_at IS NULL").
			Where("tm.user_id = ? AND tm.role = ?", user, models.TeamRoleManager)
	}
	err := q.Count(&count).Error
	return count > 0, err
}

func (a *App) CreateInboxSavedView(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	var input struct {
		Name    string                  `json:"name"`
		TeamID  *uuid.UUID              `json:"team_id"`
		Filters models.InboxQueueFilter `json:"filters"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 || !validateInboxFilter(input.Filters) || input.Filters.MessageContains != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A name and valid filters are required", nil, "")
	}
	if err := validateInboxFieldMatchers(a.DB, org, input.Filters.Fields, a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A field filter is invalid or unavailable", nil, "")
	}
	if err := validateInboxPredicates(a.DB, org, input.Filters, a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)); err != nil {
		return a.inboxError(r, err)
	}
	if input.TeamID != nil {
		permitted, err := a.canPublishInboxView(org, user, *input.TeamID)
		if err != nil {
			return a.inboxError(r, err)
		}
		if !permitted {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Cannot publish views for this team", nil, "")
		}
		// Published views always scope to the team they are shared with.
		input.Filters.TeamID = input.TeamID
	}
	view := models.InboxSavedView{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, OwnerID: user,
		Name: input.Name, TeamID: input.TeamID, Filters: input.Filters,
	}
	if err := a.DB.Create(&view).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"view": view})
}

func (a *App) DeleteInboxSavedView(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "saved view")
	if err != nil {
		return nil
	}
	var view models.InboxSavedView
	if err := a.DB.Where("organization_id = ? AND id = ?", org, id).First(&view).Error; err != nil {
		return a.inboxError(r, err)
	}
	if view.TeamID != nil {
		permitted, err := a.canPublishInboxView(org, user, *view.TeamID)
		if err != nil {
			return a.inboxError(r, err)
		}
		if !permitted {
			return a.inboxError(r, gorm.ErrRecordNotFound)
		}
	} else if view.OwnerID != user {
		return a.inboxError(r, gorm.ErrRecordNotFound)
	}
	if err := a.DB.Delete(&view).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]bool{"deleted": true})
}

type bulkInboxItem struct {
	ID      uuid.UUID `json:"id"`
	Version int64     `json:"version"`
}

type bulkInboxRequest struct {
	Items          []bulkInboxItem `json:"items"`
	Action         string          `json:"action"`
	Tag            string          `json:"tag"`
	OverrideReason string          `json:"override_reason,omitempty"`
	AssigneeID     *uuid.UUID      `json:"assignee_id"`
}

func (a *App) BulkUpdateInboxCases(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionWrite)
	if !ok {
		return nil
	}
	var input bulkInboxRequest
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if len(input.Items) == 0 || len(input.Items) > 50 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Select 1 to 50 cases", nil, "")
	}
	switch input.Action {
	case "assign", "tag":
		if !a.HasPermission(user, models.ResourceContacts, models.ActionWrite, org) {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
		}
		input.OverrideReason = strings.TrimSpace(input.OverrideReason)
		if len(input.OverrideReason) > 500 || (input.OverrideReason != "" && (input.Action != "assign" || !a.HasPermission(user, models.ResourceTeams, models.ActionWrite, org))) {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "A permitted supervisor and reason are required for a capacity override", nil, "")
		}
	case "resolve":
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid bulk action", nil, "")
	}
	results := make([]map[string]any, 0, len(input.Items))
	for _, item := range input.Items {
		result := map[string]any{"id": item.ID}
		var identity inboxQueueRow
		lookup := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org).Where("inbox_cases.id = ?", item.ID).Select("inbox_cases.*,wa.name AS account_name,c.profile_name AS contact_name").Scan(&identity)
		if lookup.Error != nil {
			return a.inboxError(r, lookup.Error)
		}
		if identity.ID != uuid.Nil {
			name := identity.ContactName
			if a.ShouldMaskPhoneNumbers(org) {
				name = utils.MaskIfPhoneNumber(name)
			}
			result["contact_id"], result["account_id"], result["account_name"], result["contact_name"] = identity.ContactID, identity.AccountID, identity.AccountName, name
		}
		err := a.updateInboxCaseInBulk(org, user, item, input)
		state := "updated"
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			state = "unavailable"
		case errors.Is(err, errInboxConflict):
			state = "stale"
		case errors.Is(err, errInboxTransition):
			state = "invalid"
		case errors.Is(err, errInboxAgentAtCapacity):
			state = "capacity"
		case errors.Is(err, errRequiredInboxFields):
			state = "required_fields"
		case err != nil:
			state = "failed"
			a.Log.Error("Inbox bulk action failed", "case_id", item.ID, "error", err)
		}
		result["result"] = state
		results = append(results, result)
	}
	return r.SendEnvelope(map[string]any{"results": results})
}

func (a *App) updateInboxCaseInBulk(org, user uuid.UUID, item bulkInboxItem, input bulkInboxRequest) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, org, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxConflict
		}
		var c models.InboxCase
		if err := a.scopeInboxCases(tx.Model(&c), user, org).Where("inbox_cases.id = ?", item.ID).First(&c).Error; err != nil {
			return err
		}
		t := inboxThread{org, c.AccountID, c.ContactID}
		if err := t.lock(tx); err != nil {
			return err
		}
		c, err = t.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		if c.ID != item.ID || c.Version != item.Version || c.Status == models.InboxCaseResolved {
			return errInboxConflict
		}
		// Recheck rights after waiting for a concurrent assignment to finish.
		var checked models.InboxCase
		if err := a.scopeInboxCases(tx.Model(&checked), user, org).Where("inbox_cases.id = ?", item.ID).First(&checked).Error; err != nil {
			return err
		}
		switch input.Action {
		case "resolve":
			return applyInboxTransition(tx, &c, inboxCaseUpdate{Status: models.InboxCaseResolved}, &user, time.Now().UTC(), "updated")
		case "assign":
			if input.AssigneeID == nil {
				return errInboxTransition
			}
			return assignInboxCase(tx, &c, user, *input.AssigneeID, input.OverrideReason)
		case "tag":
			var contact models.Contact
			if err := tx.First(&contact, "organization_id = ? AND id = ?", org, c.ContactID).Error; err != nil {
				return err
			}
			return tagInboxCase(tx, &c, &contact, user, input.Tag)
		}
		return errInboxTransition
	})
}

func assignInboxCase(tx *gorm.DB, c *models.InboxCase, user, assignee uuid.UUID, override string) error {
	var member models.UserOrganization
	if err := tx.Joins("JOIN users u ON u.id = user_organizations.user_id AND u.is_active AND u.deleted_at IS NULL").
		Where("user_organizations.organization_id = ? AND user_organizations.user_id = ?", c.OrganizationID, assignee).First(&member).Error; err != nil {
		return err
	}
	if c.TeamID != nil {
		var teamMember models.TeamMember
		if err := tx.Where("team_id = ? AND user_id = ?", c.TeamID, assignee).First(&teamMember).Error; err != nil {
			return err
		}
	}
	kind, overrideDetail := "assigned", ""
	if err := reserveInboxAgent(tx, *c, assignee); err != nil {
		if !errors.Is(err, errInboxAgentAtCapacity) || override == "" {
			return err
		}
		kind, overrideDetail = "capacity_override", override
	}
	c.AssigneeID, c.RoutingReason = &assignee, ""
	c.Version++
	if err := tx.Save(c).Error; err != nil {
		return err
	}
	return appendInboxActivity(tx, *c, models.InboxCaseActivity{ActorID: &user, Kind: kind, Detail: overrideDetail})
}

func tagInboxCase(tx *gorm.DB, c *models.InboxCase, contact *models.Contact, user uuid.UUID, name string) error {
	var tag models.Tag
	if err := tx.Where("organization_id = ? AND name = ?", c.OrganizationID, name).First(&tag).Error; err != nil {
		return err
	}
	for _, value := range contact.Tags {
		if value == tag.Name {
			return nil
		}
	}
	values := append(contact.Tags, tag.Name)
	if err := tx.Model(contact).Update("tags", values).Error; err != nil {
		return err
	}
	contact.Tags = values
	c.Version++
	if err := tx.Save(c).Error; err != nil {
		return err
	}
	return appendInboxActivity(tx, *c, models.InboxCaseActivity{ActorID: &user, Kind: "tagged"})
}

func (a *App) inboxQueueQuery(org, user uuid.UUID, f models.InboxQueueFilter) (*gorm.DB, error) {
	q := a.scopeInboxCases(a.DB.Model(&models.InboxCase{}), user, org)
	q = q.Where("NOT EXISTS (SELECT 1 FROM inbox_cases newer WHERE newer.organization_id = inbox_cases.organization_id " +
		"AND newer.account_id = inbox_cases.account_id AND newer.contact_id = inbox_cases.contact_id AND newer.deleted_at IS NULL " +
		"AND (newer.opened_at, newer.id) > (inbox_cases.opened_at, inbox_cases.id))")
	q = applyInboxQueueKind(q, f.Queue, user)
	if f.AccountID != nil {
		q = q.Where("inbox_cases.account_id = ?", f.AccountID)
	}
	if f.TeamID != nil {
		q = q.Where("inbox_cases.team_id = ?", f.TeamID)
	}
	if f.AssigneeID != nil {
		q = q.Where("inbox_cases.assignee_id = ?", f.AssigneeID)
	}
	if f.Status != "" {
		q = q.Where("inbox_cases.status = ?", f.Status)
	}
	if f.Priority != "" {
		q = q.Where("inbox_cases.priority = ?", f.Priority)
	}
	for _, tag := range f.Tags {
		value, err := json.Marshal([]string{tag})
		if err != nil {
			return nil, err
		}
		q = q.Where("c.tags @> ?::jsonb", string(value))
	}
	allowContact := a.HasPermission(user, models.ResourceContacts, models.ActionRead, org)
	if err := validateInboxFieldMatchers(a.DB, org, f.Fields, allowContact); err != nil {
		return nil, errInboxTransition
	}
	q, err := queryInboxFieldMatchers(q, org, f.Fields, allowContact)
	if err != nil {
		return nil, err
	}
	q, err = applyInboxPredicates(a.DB, q, org, f, allowContact)
	if err != nil {
		return nil, err
	}

	return q, nil
}

const inboxQueueOwnerJoin = "LEFT JOIN users inbox_owner ON inbox_owner.id = inbox_cases.assignee_id AND inbox_owner.deleted_at IS NULL AND EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.deleted_at IS NULL AND uo.user_id = inbox_owner.id AND uo.organization_id = inbox_cases.organization_id)"
const inboxQueueTeamJoin = "LEFT JOIN teams inbox_team ON inbox_team.id = inbox_cases.team_id AND inbox_team.organization_id = inbox_cases.organization_id AND inbox_team.deleted_at IS NULL"
const inboxQueueColumns = "inbox_cases.*, wa.name AS account_name, c.profile_name AS contact_name, COALESCE(inbox_owner.full_name,'') AS assignee_name, COALESCE(inbox_team.name,'') AS team_name, COALESCE((SELECT left(m.content,160) FROM messages m WHERE m.organization_id = inbox_cases.organization_id AND m.contact_id = inbox_cases.contact_id AND m.whats_app_account = wa.name AND m.direction = 'incoming' AND COALESCE(m.channel,'') IN ('','whatsapp') AND m.deleted_at IS NULL ORDER BY m.created_at DESC,m.id DESC LIMIT 1),'') AS message_preview"

func applyInboxQueueKind(q *gorm.DB, queue string, user uuid.UUID) *gorm.DB {
	switch queue {
	case "mine":
		q = q.Where("inbox_cases.assignee_id = ?", user)
	case "unassigned":
		q = q.Where("inbox_cases.assignee_id IS NULL AND inbox_cases.status <> ?", models.InboxCaseResolved)
	case "awaiting_reply":
		q = q.Where("inbox_cases.first_human_reply_at IS NULL AND inbox_cases.last_inbound_at IS NOT NULL AND inbox_cases.status IN ?",
			[]models.InboxCaseStatus{models.InboxCaseOpen, models.InboxCasePending})
	case "follow_ups":
		q = q.Where("inbox_cases.status IN ?", []models.InboxCaseStatus{models.InboxCaseWaiting, models.InboxCaseSnoozed})
	}
	return q
}

func (a *App) accessibleInboxViews(db *gorm.DB, org, user uuid.UUID) *gorm.DB {
	q := db.Where("organization_id = ?", org)
	q = q.Where("owner_id = ? OR team_id IN (SELECT tm.team_id FROM team_members tm JOIN teams t ON t.id = tm.team_id "+
		"WHERE tm.user_id = ? AND t.organization_id = ? AND t.is_active AND t.deleted_at IS NULL AND tm.deleted_at IS NULL)", user, user, org)
	return q
}
