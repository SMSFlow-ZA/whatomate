package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxtext"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type inboxMessageVariable struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	Archived bool   `json:"archived,omitempty"`
}

func inboxBuiltinVariables() []inboxMessageVariable {
	return []inboxMessageVariable{
		{Key: "contact_name", Label: "Customer name", Kind: "text", Scope: "contact"},
		{Key: "phone_number", Label: "Customer phone number", Kind: "text", Scope: "contact"},
		{Key: "agent_name", Label: "Executing user's name", Kind: "text", Scope: "agent"},
		{Key: "account_name", Label: "WhatsApp account", Kind: "text", Scope: "account"},
		{Key: "case_id", Label: "Case reference", Kind: "text", Scope: "case"},
		{Key: "case_status", Label: "Case status", Kind: "text", Scope: "case"},
		{Key: "case_priority", Label: "Case priority", Kind: "text", Scope: "case"},
	}
}

// A failed privacy-policy lookup must not expose unmasked recipient metadata.
func inboxMessageMasking(db *gorm.DB, org uuid.UUID) (bool, error) {
	var organization models.Organization
	if err := db.Select("id,settings").First(&organization, "id=?", org).Error; err != nil {
		return true, err
	}
	mask, _ := organization.Settings["mask_phone_numbers"].(bool)
	return mask, nil
}

// Catalogues expose definitions only, never a contact's values or credentials.
func (a *App) ListInboxMessageVariables(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	variables := inboxBuiltinVariables()
	mask, err := inboxMessageMasking(a.DB, org)
	if err != nil {
		return a.inboxError(r, err)
	}
	if mask {
		variables = append(variables[:1], variables[2:]...)
	}
	var definitions []models.InboxFieldDefinition
	q := a.DB.Where("organization_id = ? AND archived_at IS NULL", org)
	if !a.HasPermission(user, models.ResourceContacts, models.ActionRead, org) {
		q = q.Where("scope = 'case'")
	}
	if err := q.Order("scope,key").Find(&definitions).Error; err != nil {
		return a.inboxError(r, err)
	}
	for _, d := range definitions {
		variables = append(variables, inboxMessageVariable{Key: d.Scope + ".field." + d.Key, Label: d.Label, Kind: d.Kind, Scope: d.Scope})
	}
	return r.SendEnvelope(map[string]any{"variables": variables})
}

func (a *App) inboxMessageValues(db *gorm.DB, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, actor uuid.UUID, keys []string, deferred ...map[string]bool) (map[string]inboxtext.Value, error) {
	if account.ID != c.AccountID || account.OrganizationID != c.OrganizationID || contact.ID != c.ContactID || contact.OrganizationID != c.OrganizationID {
		return nil, errInboxTransition
	}
	mask, err := inboxMessageMasking(db, c.OrganizationID)
	if err != nil {
		return nil, err
	}
	values := map[string]inboxtext.Value{
		"contact_name": {Text: contact.ProfileName}, "phone_number": {Text: contact.PhoneNumber},
		"agent_name": {Reason: "unavailable"}, "user_name": {Reason: "unavailable"},
		"account_name": {Text: account.Name}, "case_id": {Text: c.ID.String()},
		"case_status": {Text: string(c.Status)}, "case_priority": {Text: string(c.Priority)},
	}
	if mask {
		values["phone_number"] = inboxtext.Value{Reason: "unavailable"}
		if utils.MaskIfPhoneNumber(contact.ProfileName) != contact.ProfileName {
			values["contact_name"] = inboxtext.Value{Reason: "unavailable"}
		}
	}
	needActor, fieldKeys := false, []string{}
	for _, key := range keys {
		if key == "agent_name" || key == "user_name" {
			needActor = true
		}
		if _, field, found := strings.Cut(key, ".field."); found {
			fieldKeys = append(fieldKeys, field)
		}
	}
	if needActor && actor != uuid.Nil {
		var agent models.User
		err := db.Select("id,full_name").Where("id=? AND is_active AND (is_super_admin OR organization_id=? OR EXISTS (SELECT 1 FROM user_organizations uo WHERE uo.user_id=users.id AND uo.organization_id=? AND uo.deleted_at IS NULL))", actor, c.OrganizationID, c.OrganizationID).First(&agent).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err == nil {
			values["agent_name"], values["user_name"] = inboxtext.Value{Text: agent.FullName}, inboxtext.Value{Text: agent.FullName}
		}
	}
	if len(fieldKeys) == 0 {
		return values, nil
	}
	definitions := []models.InboxFieldDefinition{}
	q := db.Where("organization_id=? AND key IN ?", c.OrganizationID, fieldKeys)
	viewer := auditViewer{}
	if actor != uuid.Nil {
		viewer, err = loadAuditViewer(db, c.OrganizationID, actor, nil)
		if err != nil {
			return nil, err
		}
	}
	if !viewer.has(models.ResourceContacts, models.ActionRead) {
		q = q.Where("scope='case'")
	}
	if err := q.Find(&definitions).Error; err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(definitions))
	for _, d := range definitions {
		ids = append(ids, d.ID)
	}
	stored := []models.InboxFieldValue{}
	if len(ids) > 0 {
		if err := db.Where("organization_id=? AND definition_id IN ? AND subject_id IN ?", c.OrganizationID, ids, []uuid.UUID{c.ID, c.ContactID}).Find(&stored).Error; err != nil {
			return nil, err
		}
	}
	bySubject := map[uuid.UUID]map[uuid.UUID]json.RawMessage{}
	for _, v := range stored {
		if bySubject[v.SubjectID] == nil {
			bySubject[v.SubjectID] = map[uuid.UUID]json.RawMessage{}
		}
		bySubject[v.SubjectID][v.DefinitionID] = v.Value
	}
	for _, d := range definitions {
		key, subject := d.Scope+".field."+d.Key, c.ID
		if d.Scope == "contact" {
			subject = c.ContactID
		}
		value := inboxtext.Value{Reason: "empty"}
		if d.ArchivedAt != nil {
			value.Reason = "archived"
		} else if raw := bySubject[subject][d.ID]; len(raw) != 0 && string(bytes.TrimSpace(raw)) != "null" {
			if d.ValidateValue(raw) != nil {
				value.Reason = "invalid"
			} else {
				var scalar any
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				if err := decoder.Decode(&scalar); err != nil {
					return nil, err
				}
				switch v := scalar.(type) {
				case string:
					value = inboxtext.Value{Text: v}
				case json.Number:
					value = inboxtext.Value{Text: string(v)}
				case bool:
					value = inboxtext.Value{Text: "false"}
					if v {
						value.Text = "true"
					}
				}
			}
		}
		values[key] = value
	}
	// Only an existing, permitted field from an earlier validated output can
	// become deferred. Missing permissions, archived fields and masking stay
	// unavailable. The marker is presentation, never a dispatchable value.
	if len(deferred) == 1 {
		for key := range deferred[0] {
			value, exists := values[key]
			if exists && (value.Reason == "" || value.Reason == "empty") {
				values[key] = inboxtext.Value{Text: "…"}
			}
		}
	}
	return values, nil
}

func (a *App) renderInboxMessage(db *gorm.DB, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, actor uuid.UUID, text string, deferred ...map[string]bool) (inboxtext.Result, error) {
	keys, err := inboxtext.Keys(text)
	if err != nil {
		return inboxtext.Result{}, err
	}
	values, err := a.inboxMessageValues(db, c, account, contact, actor, keys, deferred...)
	if err != nil {
		return inboxtext.Result{}, err
	}
	return inboxtext.Render(text, values)
}
