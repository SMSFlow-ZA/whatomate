package handlers

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
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
	definitions, err := inboxPredicateDefinitions(db, c.OrganizationID, predicates, allowContact)
	if err != nil {
		return nil, err
	}
	selections := []string{}
	args := []any{}
	for i, p := range predicates {
		expression, values, err := compiledInboxPredicateSQL(p, definitions)
		if err != nil {
			return nil, err
		}
		selections = append(selections, "("+expression+") AS "+fmt.Sprintf("p%d", i))
		args = append(args, values...)
	}
	flags := map[string]interface{}{}
	if len(selections) > 0 {
		if err := db.Model(&models.InboxCase{}).Joins("JOIN contacts c ON c.id=inbox_cases.contact_id AND c.organization_id=inbox_cases.organization_id AND c.deleted_at IS NULL").Where("inbox_cases.organization_id=? AND inbox_cases.id=?", c.OrganizationID, c.ID).Select(strings.Join(selections, ","), args...).Scan(&flags).Error; err != nil {
			return nil, err
		}
	}
	for i, p := range predicates {
		label := ""
		if p.Field == "custom_field" {
			label = definitions[*p.DefinitionID].Label
		}
		matched, _ := flags[fmt.Sprintf("p%d", i)].(bool)
		add(p.Field, label, groups[i], matched)
	}
	if f.MessageContains != "" {
		add("message_contains", "", "all", len(triggerText) == 1 && strings.Contains(strings.ToLower(triggerText[0]), strings.ToLower(f.MessageContains)))
	}
	return out, nil
}
