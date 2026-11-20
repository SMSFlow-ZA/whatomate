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
	Type        string `json:"type"`
	Text        string `json:"text"`
	Header      string `json:"header,omitempty"`
	Footer      string `json:"footer,omitempty"`
	Template    string `json:"template,omitempty"`
	TemplateKey string `json:"template_key,omitempty"`
	Language    string `json:"language,omitempty"`
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
	return previewInboxReplyWithDB(a.DB, c, account, contact, action, now)
}

type inboxEligibilityError struct{ reason string }

func (e inboxEligibilityError) Error() string { return errInboxSendIneligible.Error() }
func (e inboxEligibilityError) Unwrap() error { return errInboxSendIneligible }
func inboxEligibilityReason(err error) string {
	var problem inboxEligibilityError
	if errors.As(err, &problem) {
		return problem.reason
	}
	return "unavailable"
}

func previewInboxReplyWithDB(db *gorm.DB, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, action models.InboxAction, now time.Time) (inboxReplyPreview, error) {
	intent := inboxReplyPreview{Type: "text", Text: action.Value}
	if account.ID != c.AccountID || account.OrganizationID != c.OrganizationID || contact.ID != c.ContactID || contact.OrganizationID != c.OrganizationID {
		return intent, inboxEligibilityError{"unavailable"}
	}
	if action.Type == "send_template" {
		intent = inboxReplyPreview{Type: "template", TemplateKey: action.Value}
		id, err := uuid.Parse(action.Value)
		if err != nil {
			return intent, inboxEligibilityError{"template_unavailable"}
		}
		var template models.Template
		err = db.Where("organization_id = ? AND whats_app_account = ? AND id = ?", c.OrganizationID, account.Name, id).First(&template).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return intent, inboxEligibilityError{"template_unavailable"}
		}
		if err != nil {
			return intent, err
		}
		intent = inboxReplyPreview{Type: "template", Text: template.BodyContent, Header: template.HeaderContent, Footer: template.FooterContent, Template: template.DisplayName, TemplateKey: template.Name, Language: template.Language}
		if template.Status != "APPROVED" || template.Category != "UTILITY" {
			return intent, inboxEligibilityError{"template_not_approved"}
		}
		if !action.ConsentConfirmed || contact.MarketingOptOut {
			return intent, inboxEligibilityError{"consent_missing"}
		}
		if (template.HeaderType != "" && template.HeaderType != "TEXT") || strings.Contains(template.HeaderContent, "{{") || strings.Contains(template.BodyContent, "{{") || strings.Contains(template.FooterContent, "{{") || len(template.Buttons) != 0 {
			return intent, inboxEligibilityError{"template_not_supported"}
		}
	}
	if account.ID != c.AccountID || account.OrganizationID != c.OrganizationID || contact.ID != c.ContactID || contact.OrganizationID != c.OrganizationID {
		return intent, inboxEligibilityError{"unavailable"}
	}
	if account.Status != "active" {
		return intent, inboxEligibilityError{"account_inactive"}
	}
	if !eligibleInboxNumber(contact.PhoneNumber) {
		return intent, inboxEligibilityError{"recipient_invalid"}
	}
	if action.Type == "send_text" {
		var inbound models.Message
		err := db.Select("created_at").Where("organization_id = ? AND contact_id = ? AND whats_app_account = ? AND direction = ? AND COALESCE(channel, '') IN ('', 'whatsapp')", c.OrganizationID, c.ContactID, account.Name, models.DirectionIncoming).Order("created_at DESC, id DESC").First(&inbound).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return intent, inboxEligibilityError{"service_window_closed"}
		}
		if err != nil {
			return intent, err
		}
		if now.Before(inbound.CreatedAt) || !now.Before(inbound.CreatedAt.Add(24*time.Hour)) {
			return intent, inboxEligibilityError{"service_window_closed"}
		}
	} else if action.Type != "send_template" {
		return intent, inboxEligibilityError{"unavailable"}
	}
	return intent, nil
}

func eligibleInboxNumber(phone string) bool {
	digits := strings.TrimPrefix(phone, "+")
	return len(digits) >= 7 && len(digits) <= 15 && strings.IndexFunc(digits, func(ch rune) bool { return ch < '0' || ch > '9' }) == -1
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
		CaseID      uuid.UUID `json:"case_id"`
		TriggerText string    `json:"trigger_text"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if len(input.TriggerText) > 4096 {
		return a.inboxError(r, errInboxTransition)
	}
	var workflow models.InboxWorkflow
	if err := a.visibleInboxWorkflows(a.DB, org, user).Where("id = ?", id).First(&workflow).Error; err != nil {
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
	if err := a.DB.Select("id", "organization_id", "name", "status", "phone_id").
		Where("id = ? AND organization_id = ?", c.AccountID, org).First(&account).Error; err != nil {
		return a.inboxError(r, err)
	}
	name, phone := contact.ProfileName, contact.PhoneNumber
	if a.ShouldMaskPhoneNumbers(org) {
		name = utils.MaskIfPhoneNumber(name)
		phone = utils.MaskPhoneNumber(phone)
	}
	preview, err := a.evaluateInboxWorkflow(a.DB, workflow, c, account, contact, user, time.Now().UTC(), input.TriggerText)
	if err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{
		"matches": preview.Matches, "case_id": c.ID, "case_version": c.Version, "actions": workflow.Actions,
		"recipient": map[string]string{"name": name, "phone": phone}, "account": account.Name,
		"reply": preview.Reply, "send_unavailable": preview.SendUnavailable, "send_unavailable_reason": preview.SendUnavailableReason, "confirmation": preview.Confirmation,
		"condition_outcomes": preview.ConditionOutcomes, "action_outcomes": preview.ActionOutcomes, "actions_eligible": preview.ActionsEligible, "trigger_text_is_synthetic": input.TriggerText != "",
	})
}
