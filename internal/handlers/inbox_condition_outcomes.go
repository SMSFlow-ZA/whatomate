package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"strings"
)

func (a *App) inboxConditionOutcomes(db *gorm.DB, w models.InboxWorkflow, c models.InboxCase, contact models.Contact, user uuid.UUID, triggerText []string) ([]models.InboxConditionOutcome, error) {
	out := []models.InboxConditionOutcome{}
	add := func(field, label, group string, matched bool) {
		out = append(out, models.InboxConditionOutcome{Field: field, Label: label, Group: group, Matched: matched})
	}
	f := w.Conditions
	if f.AccountID != nil {
		add("account", "", "all", *f.AccountID == c.AccountID)
	}
	if f.TeamID != nil {
		add("team", "", "all", c.TeamID != nil && *f.TeamID == *c.TeamID)
	}
	if f.AssigneeID != nil {
		add("assignee", "", "all", c.AssigneeID != nil && *f.AssigneeID == *c.AssigneeID)
	}
	if f.Status != "" {
		add("status", "", "all", f.Status == c.Status)
	}
	if f.Priority != "" {
		add("priority", "", "all", f.Priority == c.Priority)
	}
	for _, tag := range f.Tags {
		matched := false
		for _, value := range contact.Tags {
			matched = matched || tag == value
		}
		add("tag", tag, "all", matched)
	}
	allowContact := a.HasPermission(user, models.ResourceContacts, models.ActionRead, c.OrganizationID)
	predicates := append([]models.InboxPredicate{}, f.Predicates...)
	groups := make([]string, len(predicates))
	for i := range groups {
		groups[i] = f.Match
		if groups[i] == "" {
			groups[i] = "all"
		}
	}
	for _, field := range f.Fields {
		id := field.DefinitionID
		predicates = append(predicates, models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Operator: field.Operator, Value: field.Value})
		groups = append(groups, "all")
	}
	for i, p := range predicates {
		sql, args, err := inboxPredicateSQL(db, c.OrganizationID, p, allowContact)
		if err != nil {
			return nil, err
		}
		var count int64
		if err := db.Model(&models.InboxCase{}).Joins("JOIN contacts c ON c.id=inbox_cases.contact_id AND c.organization_id=inbox_cases.organization_id AND c.deleted_at IS NULL").Where("inbox_cases.organization_id=? AND inbox_cases.id=?", c.OrganizationID, c.ID).Where(sql, args...).Count(&count).Error; err != nil {
			return nil, err
		}
		label := ""
		if p.Field == "custom_field" {
			var definition models.InboxFieldDefinition
			if err := db.First(&definition, "id=? AND organization_id=?", p.DefinitionID, c.OrganizationID).Error; err != nil {
				return nil, err
			}
			label = definition.Label
		}
		add(p.Field, label, groups[i], count == 1)
	}
	if f.MessageContains != "" {
		add("message_contains", "", "all", len(triggerText) == 1 && strings.Contains(strings.ToLower(triggerText[0]), strings.ToLower(f.MessageContains)))
	}
	return out, nil
}
