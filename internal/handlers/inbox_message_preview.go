package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type inboxMessageConfirmation struct {
	CannedResponseID *uuid.UUID `json:"canned_response_id,omitempty"`
	CaseID           uuid.UUID  `json:"case_id"`
	Version          int64      `json:"version"`
	AuthoredText     string     `json:"authored_text"`
	Confirmation     string     `json:"confirmation"`
}

func inboxMessageConfirmationDigest(c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, actor uuid.UUID, text string, reply inboxReplyPreview) string {
	return inboxPreviewConfirmation([]any{actor, c.ID, c.Version, text, inboxReplyFingerprint(c, account, contact, reply)})
}

func (a *App) PreviewInboxMessage(r *fastglue.Request) error {
	org, actor, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	var input struct {
		CannedResponseID *uuid.UUID `json:"canned_response_id,omitempty"`
		CaseID           uuid.UUID  `json:"case_id"`
		Text             string     `json:"text"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if input.CaseID == uuid.Nil {
		return a.inboxError(r, errInboxTransition)
	}
	var c models.InboxCase
	if err := a.scopeInboxCases(a.DB.Model(&c), actor, org).First(&c, "inbox_cases.id=?", input.CaseID).Error; err != nil {
		return a.inboxError(r, err)
	}
	latest, err := (inboxThread{org, c.AccountID, c.ContactID}).latest(a.DB)
	if err != nil {
		return a.inboxError(r, err)
	}
	if latest.ID != c.ID || c.Status == models.InboxCaseResolved {
		return a.inboxError(r, errInboxConflict)
	}
	account, contact, err := a.inboxWorkflowContext(a.DB, c)
	if err != nil {
		return a.inboxError(r, err)
	}
	reply, authored, previewErr := a.previewInboxPersonalisedSource(a.DB, c, account, contact, actor, input.Text, input.CannedResponseID)
	if previewErr != nil && !errors.Is(previewErr, errInboxSendIneligible) {
		return a.inboxError(r, previewErr)
	}
	name, phone := contact.ProfileName, contact.PhoneNumber
	mask, err := inboxMessageMasking(a.DB, org)
	if err != nil {
		return a.inboxError(r, err)
	}
	if mask {
		name, phone = utils.MaskIfPhoneNumber(name), utils.MaskPhoneNumber(phone)
	}
	confirmation := ""
	reason := ""
	if previewErr == nil {
		confirmation = inboxMessageConfirmationDigest(c, account, contact, actor, authored, reply)
	} else {
		reason = inboxEligibilityReason(previewErr)
	}
	return r.SendEnvelope(map[string]any{
		"case_id": c.ID, "case_version": c.Version, "matches": true, "actions": []models.InboxAction{},
		"account": account.Name, "recipient": map[string]string{"name": name, "phone": phone}, "reply": reply,
		"send_unavailable": previewErr != nil, "send_unavailable_reason": reason, "confirmation": confirmation,
	})
}

func (a *App) confirmInboxMessage(db *gorm.DB, org, actor uuid.UUID, account models.WhatsAppAccount, contact models.Contact, input inboxMessageConfirmation) (string, error) {
	reply, err := a.confirmInboxReply(db, org, actor, account, contact, input)
	return reply.Text, err
}

func (a *App) confirmInboxReply(db *gorm.DB, org, actor uuid.UUID, account models.WhatsAppAccount, contact models.Contact, input inboxMessageConfirmation) (inboxReplyPreview, error) {
	if !a.HasPermission(actor, models.ResourceChat, models.ActionWrite, org) {
		return inboxReplyPreview{}, errInboxTransition
	}
	if len(input.Confirmation) != 64 || input.CaseID == uuid.Nil {
		return inboxReplyPreview{}, errInboxTransition
	}
	var c models.InboxCase
	if err := a.scopeInboxCases(db.Model(&c), actor, org).First(&c, "inbox_cases.id=? AND inbox_cases.account_id=? AND inbox_cases.contact_id=?", input.CaseID, account.ID, contact.ID).Error; err != nil {
		return inboxReplyPreview{}, err
	}
	latest, err := (inboxThread{org, account.ID, contact.ID}).latest(db)
	if err != nil {
		return inboxReplyPreview{}, err
	}
	if latest.ID != c.ID || c.Status == models.InboxCaseResolved || c.Version != input.Version {
		return inboxReplyPreview{}, errInboxConflict
	}
	if enabled, err := inboxWorkflowEnabled(db, org, false); err != nil {
		return inboxReplyPreview{}, err
	} else if !enabled {
		return inboxReplyPreview{}, errInboxDisabled
	}
	reply, authored, err := a.previewInboxPersonalisedSource(db, c, account, contact, actor, input.AuthoredText, input.CannedResponseID)
	if err != nil {
		return inboxReplyPreview{}, err
	}
	if !inboxPreviewMatches(inboxMessageConfirmationDigest(c, account, contact, actor, authored, reply), input.Confirmation) {
		return inboxReplyPreview{}, errInboxConflict
	}
	return reply, nil
}

func (a *App) previewInboxPersonalisedSource(db *gorm.DB, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, actor uuid.UUID, text string, canned *uuid.UUID) (inboxReplyPreview, string, error) {
	if canned != nil {
		if text != "" {
			return inboxReplyPreview{}, "", errInboxTransition
		}
		return a.previewPersonalisedCanned(db, c, account, contact, actor, *canned, time.Now().UTC())
	}
	action := models.InboxAction{Type: "send_text", Value: text, Personalise: true}
	if !validInboxPersonalisation(action) {
		return inboxReplyPreview{}, "", errInboxTransition
	}
	reply, err := a.previewInboxReplyForActor(db, c, account, contact, action, time.Now().UTC(), actor)
	return reply, text, err
}
