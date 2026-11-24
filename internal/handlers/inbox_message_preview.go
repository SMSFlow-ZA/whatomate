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
	CaseID       uuid.UUID `json:"case_id"`
	Version      int64     `json:"version"`
	AuthoredText string    `json:"authored_text"`
	Confirmation string    `json:"confirmation"`
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
		CaseID uuid.UUID `json:"case_id"`
		Text   string    `json:"text"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	action := models.InboxAction{Type: "send_text", Value: input.Text, Personalise: true}
	if input.CaseID == uuid.Nil || !validInboxPersonalisation(action) {
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
	reply, previewErr := a.previewInboxReplyForActor(a.DB, c, account, contact, action, time.Now().UTC(), actor)
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
		confirmation = inboxMessageConfirmationDigest(c, account, contact, actor, input.Text, reply)
	} else {
		reason = inboxEligibilityReason(previewErr)
	}
	return r.SendEnvelope(map[string]any{
		"case_id": c.ID, "case_version": c.Version, "matches": true, "actions": []models.InboxAction{action},
		"account": account.Name, "recipient": map[string]string{"name": name, "phone": phone}, "reply": reply,
		"send_unavailable": previewErr != nil, "send_unavailable_reason": reason, "confirmation": confirmation,
	})
}

func (a *App) confirmInboxMessage(db *gorm.DB, org, actor uuid.UUID, account models.WhatsAppAccount, contact models.Contact, input inboxMessageConfirmation) (string, error) {
	if !a.HasPermission(actor, models.ResourceChat, models.ActionWrite, org) {
		return "", errInboxTransition
	}
	if len(input.Confirmation) != 64 || input.CaseID == uuid.Nil {
		return "", errInboxTransition
	}
	var c models.InboxCase
	if err := a.scopeInboxCases(db.Model(&c), actor, org).First(&c, "inbox_cases.id=? AND inbox_cases.account_id=? AND inbox_cases.contact_id=?", input.CaseID, account.ID, contact.ID).Error; err != nil {
		return "", err
	}
	latest, err := (inboxThread{org, account.ID, contact.ID}).latest(db)
	if err != nil {
		return "", err
	}
	if latest.ID != c.ID || c.Status == models.InboxCaseResolved || c.Version != input.Version {
		return "", errInboxConflict
	}
	if enabled, err := inboxWorkflowEnabled(db, org, false); err != nil {
		return "", err
	} else if !enabled {
		return "", errInboxDisabled
	}
	action := models.InboxAction{Type: "send_text", Value: input.AuthoredText, Personalise: true}
	if !validInboxPersonalisation(action) {
		return "", errInboxTransition
	}
	reply, err := a.previewInboxReplyForActor(db, c, account, contact, action, time.Now().UTC(), actor)
	if err != nil {
		return "", err
	}
	if !inboxPreviewMatches(inboxMessageConfirmationDigest(c, account, contact, actor, input.AuthoredText, reply), input.Confirmation) {
		return "", errInboxConflict
	}
	return reply.Text, nil
}
