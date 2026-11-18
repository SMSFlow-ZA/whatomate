package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

var errInboxSendIneligible = errors.New("message is not eligible for this conversation")

type inboxReplyPreview struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Header   string `json:"header,omitempty"`
	Footer   string `json:"footer,omitempty"`
	Template string `json:"template,omitempty"`
	Language string `json:"language,omitempty"`
}

func workflowMatchesCase(conditions models.InboxQueueFilter, c models.InboxCase, contact models.Contact) bool {
	if (conditions.AccountID != nil && *conditions.AccountID != c.AccountID) ||
		(conditions.TeamID != nil && (c.TeamID == nil || *conditions.TeamID != *c.TeamID)) ||
		(conditions.AssigneeID != nil && (c.AssigneeID == nil || *conditions.AssigneeID != *c.AssigneeID)) ||
		(conditions.Status != "" && conditions.Status != c.Status) ||
		(conditions.Priority != "" && conditions.Priority != c.Priority) {
		return false
	}
	for _, expected := range conditions.Tags {
		matched := false
		for _, actual := range contact.Tags {
			if actual == expected {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (a *App) previewInboxReply(c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, action models.InboxAction, now time.Time) (inboxReplyPreview, error) {
	// A customer message on another business number cannot open this account's
	// service window. Keep the check tied to the same thread as the intended send.
	if account.ID != c.AccountID || account.OrganizationID != c.OrganizationID || contact.ID != c.ContactID ||
		contact.OrganizationID != c.OrganizationID || account.Status != "active" || contact.PhoneNumber == "" {
		return inboxReplyPreview{}, errInboxSendIneligible
	}
	switch action.Type {
	case "send_text":
		var inbound models.Message
		err := a.DB.Select("created_at").Where("organization_id = ? AND contact_id = ? AND whats_app_account = ? AND direction = ?",
			c.OrganizationID, c.ContactID, account.Name, models.DirectionIncoming).Order("created_at DESC, id DESC").First(&inbound).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return inboxReplyPreview{}, errInboxSendIneligible
		}
		if err != nil {
			return inboxReplyPreview{}, err
		}
		if now.Before(inbound.CreatedAt) || !now.Before(inbound.CreatedAt.Add(24*time.Hour)) {
			return inboxReplyPreview{}, errInboxSendIneligible
		}
		return inboxReplyPreview{Type: "text", Text: action.Value}, nil
	case "send_template":
		id, err := uuid.Parse(action.Value)
		if err != nil {
			return inboxReplyPreview{}, errInboxSendIneligible
		}
		var template models.Template
		err = a.DB.Where("organization_id = ? AND whats_app_account = ? AND id = ? AND status = ? AND category = ?",
			c.OrganizationID, account.Name, id, "APPROVED", "UTILITY").First(&template).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return inboxReplyPreview{}, errInboxSendIneligible
		}
		if err != nil {
			return inboxReplyPreview{}, err
		}
		// Preview only payloads whose delivered copy can be shown in full.
		if (template.HeaderType != "" && template.HeaderType != "TEXT") ||
			strings.Contains(template.HeaderContent, "{{") || strings.Contains(template.BodyContent, "{{") ||
			strings.Contains(template.FooterContent, "{{") || len(template.Buttons) != 0 {
			return inboxReplyPreview{}, errInboxSendIneligible
		}
		return inboxReplyPreview{Type: "template", Text: template.BodyContent, Header: template.HeaderContent,
			Footer: template.FooterContent, Template: template.DisplayName, Language: template.Language}, nil
	}
	return inboxReplyPreview{}, errInboxSendIneligible
}

func (a *App) PreviewInboxWorkflow(r *fastglue.Request) error {
	org, user, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "inbox workflow")
	if err != nil {
		return nil
	}
	var input struct {
		CaseID uuid.UUID `json:"case_id"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var workflow models.InboxWorkflow
	if err := a.DB.Where("organization_id = ? AND id = ?", org, id).First(&workflow).Error; err != nil {
		return a.inboxError(r, err)
	}
	if workflow.Kind == "rule" {
		if _, _, err := a.requireAuth(r, inboxWorkflowSettingsResource, models.ActionWrite); err != nil {
			return nil
		}
	}
	var c models.InboxCase
	if err := a.scopeInboxCases(a.DB.Model(&c), user, org).Where("inbox_cases.id = ?", input.CaseID).
		First(&c).Error; err != nil {
		return a.inboxError(r, err)
	}
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", c.ContactID, org).First(&contact).Error; err != nil {
		return a.inboxError(r, err)
	}
	var account models.WhatsAppAccount
	if err := a.DB.Select("id", "organization_id", "name", "status").
		Where("id = ? AND organization_id = ?", c.AccountID, org).First(&account).Error; err != nil {
		return a.inboxError(r, err)
	}
	name, phone := contact.ProfileName, contact.PhoneNumber
	if a.ShouldMaskPhoneNumbers(org) {
		name = utils.MaskIfPhoneNumber(name)
		phone = utils.MaskPhoneNumber(phone)
	}
	matches := workflowMatchesCase(workflow.Conditions, c, contact)
	var reply *inboxReplyPreview
	sendUnavailable := false
	for _, action := range workflow.Actions {
		if action.Type != "send_text" && action.Type != "send_template" {
			continue
		}
		preview, err := a.previewInboxReply(c, account, contact, action, time.Now().UTC())
		if errors.Is(err, errInboxSendIneligible) {
			sendUnavailable = true
		} else if err != nil {
			return a.inboxError(r, err)
		} else {
			reply = &preview
		}
	}
	return r.SendEnvelope(map[string]any{
		"matches": matches, "case_id": c.ID, "case_version": c.Version, "actions": workflow.Actions,
		"recipient": map[string]string{"name": name, "phone": phone}, "account": account.Name,
		"reply": reply, "send_unavailable": sendUnavailable,
	})
}
