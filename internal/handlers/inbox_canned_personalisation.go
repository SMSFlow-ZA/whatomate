package handlers

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxtext"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Structural validation runs before values exist. Actual label lengths and URLs
// are checked again after rendering; no overlong value is silently truncated.
func validatePersonalisedCanned(content string, buttons []CannedResponseButton, enabled bool) error {
	if !enabled {
		return validateCannedResponseButtons(buttons)
	}
	if strings.TrimSpace(content) == "" || len(buttons) > maxReplyButtons {
		return inboxtext.ErrInvalid
	}
	texts := []string{content}
	structural := append([]CannedResponseButton(nil), buttons...)
	for i, button := range buttons {
		switch button.Type {
		case "", "reply", "url", "voice_call", "flow":
		default:
			return errors.New("unsupported personalised button type")
		}
		if len(button.ID) > 256 || strings.Contains(button.ID, "{{") || strings.Contains(button.FlowID, "{{") || strings.Contains(button.Screen, "{{") {
			return errors.New("button identifiers and flow routing must be literal")
		}
		texts = append(texts, button.Title, button.URL)
		if strings.Contains(button.Title, "{{") {
			structural[i].Title = "Value"
		}
		if strings.Contains(button.URL, "{{") {
			structural[i].URL = "https://example.invalid/"
		}
		if (button.Type == "" || button.Type == "reply") && strings.TrimSpace(button.ID) == "" {
			return errors.New("reply buttons need a stable identifier")
		}
	}
	if _, err := inboxtext.KeysForTexts(texts); err != nil {
		return err
	}
	return validateCannedResponseButtons(structural)
}

func (a *App) previewPersonalisedCanned(db *gorm.DB, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, actor, id uuid.UUID, now time.Time) (inboxReplyPreview, string, error) {
	if id == uuid.Nil || !a.HasPermission(actor, models.ResourceCannedResponses, models.ActionRead, c.OrganizationID) {
		return inboxReplyPreview{}, "", gorm.ErrRecordNotFound
	}
	var saved models.CannedResponse
	if err := db.Where("id=? AND organization_id=? AND is_active AND personalise", id, c.OrganizationID).First(&saved).Error; err != nil {
		return inboxReplyPreview{}, "", err
	}
	buttons := jsonbArrayToButtons(saved.Buttons)
	intent := inboxReplyPreview{Type: "text", Text: saved.Content, CannedResponseID: &saved.ID, CannedName: saved.Name,
		CannedRevision: workflowDigest([]any{saved.ID, saved.UpdatedAt, saved.Name, saved.Content, saved.Buttons, saved.IsActive, saved.Personalise})}
	if len(buttons) != len(saved.Buttons) {
		return intent, saved.Content, inboxEligibilityError{"canned_content_invalid"}
	}
	if err := validatePersonalisedCanned(saved.Content, buttons, true); err != nil {
		return intent, saved.Content, inboxEligibilityError{"variables_invalid"}
	}
	texts := []string{saved.Content}
	for _, button := range buttons {
		texts = append(texts, button.Title, button.URL)
	}
	keys, err := inboxtext.KeysForTexts(texts)
	if err != nil {
		return intent, saved.Content, inboxEligibilityError{"variables_invalid"}
	}
	values, err := a.inboxMessageValues(db, c, account, contact, actor, keys)
	if err != nil {
		return intent, saved.Content, err
	}
	render := func(text string) (string, error) {
		result, err := inboxtext.Render(text, values)
		if err != nil {
			return "", err
		}
		intent.Missing = append(intent.Missing, result.Missing...)
		return result.Text, nil
	}
	intent.Text, err = render(saved.Content)
	if err != nil {
		return intent, saved.Content, inboxEligibilityError{"variables_invalid"}
	}
	for i := range buttons {
		buttons[i].Title, err = render(buttons[i].Title)
		if err != nil {
			return intent, saved.Content, inboxEligibilityError{"variables_invalid"}
		}
		buttons[i].URL, err = render(buttons[i].URL)
		if err != nil {
			return intent, saved.Content, inboxEligibilityError{"variables_invalid"}
		}
	}
	if len(buttons) > 0 {
		intent.Type = "interactive"
		interactive := &InteractiveContent{Body: intent.Text}
		switch buttons[0].Type {
		case "url":
			interactive.Type, interactive.URL, interactive.ButtonText = "cta_url", buttons[0].URL, buttons[0].Title
		case "voice_call":
			interactive.Type, interactive.DisplayText, interactive.TTLMinutes = "voice_call", buttons[0].Title, buttons[0].TTLMinutes
		case "flow":
			interactive.Type, interactive.FlowID, interactive.FirstScreen, interactive.ButtonText = "flow", buttons[0].FlowID, buttons[0].Screen, buttons[0].Title
		default:
			interactive.Type = "button"
			if len(buttons) > 3 {
				interactive.Type = "list"
			}
			for _, button := range buttons {
				interactive.Buttons = append(interactive.Buttons, ButtonContent{ID: button.ID, Title: button.Title})
			}
		}
		intent.Interactive = interactive
	}
	if len(intent.Missing) != 0 {
		return intent, saved.Content, inboxEligibilityError{"variables_missing"}
	}
	if strings.TrimSpace(intent.Text) == "" || (len(buttons) > 0 && utf8.RuneCountInString(intent.Text) > 1024) || validateCannedResponseButtons(buttons) != nil {
		return intent, saved.Content, inboxEligibilityError{"canned_content_invalid"}
	}
	if intent.Interactive != nil {
		switch intent.Interactive.Type {
		case "voice_call":
			if !account.BusinessCallingEnabled {
				return intent, saved.Content, inboxEligibilityError{"calling_unavailable"}
			}
		case "flow":
			var count int64
			err := db.Model(&models.WhatsAppFlow{}).Where("organization_id=? AND whats_app_account=? AND meta_flow_id=? AND status='PUBLISHED'", c.OrganizationID, account.Name, intent.Interactive.FlowID).Count(&count).Error
			if err != nil {
				return intent, saved.Content, err
			}
			if count != 1 {
				return intent, saved.Content, inboxEligibilityError{"flow_unavailable"}
			}
		}
	}
	// The same ordinary service-message eligibility path governs rich replies.
	_, eligibility := a.previewInboxReplyForActor(db, c, account, contact, models.InboxAction{Type: "send_text", Value: intent.Text}, now, actor)
	return intent, saved.Content, eligibility
}
