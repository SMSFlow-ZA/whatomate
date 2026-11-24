package handlers

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxtext"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"gorm.io/gorm"
)

func inboxTemplateRevision(t models.Template) string {
	return workflowDigest([]any{t.ID, t.Name, t.Language, t.Status, t.Category, t.HeaderType, t.HeaderContent, t.BodyContent, t.FooterContent, t.Buttons})
}

func validInboxParameterName(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for _, ch := range key {
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func validInboxPersonalisation(action models.InboxAction) bool {
	if action.Type != "send_text" && action.Type != "send_template" {
		return !action.Personalise && len(action.BodyParameters) == 0 && len(action.HeaderParameters) == 0
	}
	if action.Type == "send_text" && (len(action.BodyParameters) != 0 || len(action.HeaderParameters) != 0) {
		return false
	}
	if len(action.BodyParameters) > 32 || len(action.HeaderParameters) > 1 {
		return false
	}
	texts := []string{}
	if action.Type == "send_text" {
		texts = append(texts, action.Value)
	}
	for _, parameters := range []map[string]string{action.BodyParameters, action.HeaderParameters} {
		for key, value := range parameters {
			if !validInboxParameterName(key) || strings.TrimSpace(value) == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > inboxtext.MaxCharacters {
				return false
			}
			texts = append(texts, value)
		}
	}
	if action.Personalise {
		_, err := inboxtext.KeysForTexts(texts)
		return err == nil
	}
	return true
}

// Render all mapped parameters from one authorised snapshot. Component-specific
// names remain separate: header {{1}} is not the same input as body {{1}}.
func (a *App) personaliseInboxTemplate(db *gorm.DB, c models.InboxCase, account models.WhatsAppAccount, contact models.Contact, actor uuid.UUID, action models.InboxAction, t models.Template, intent *inboxReplyPreview, deferred ...map[string]bool) error {
	intent.TemplateRevision = inboxTemplateRevision(t)
	if !validInboxPersonalisation(action) || templateutil.ValidateNoMixedParams(t.BodyContent) != nil || templateutil.ValidateNoMixedParams(t.HeaderContent) != nil || templateutil.ValidateHeaderParamCount(t.HeaderContent) != nil {
		return inboxEligibilityError{"template_not_supported"}
	}
	keys := []string{}
	if action.Personalise {
		texts := []string{}
		for _, parameters := range []map[string]string{action.BodyParameters, action.HeaderParameters} {
			for _, text := range parameters {
				texts = append(texts, text)
			}
		}
		var err error
		keys, err = inboxtext.KeysForTexts(texts)
		if err != nil {
			return inboxEligibilityError{"variables_invalid"}
		}
	}
	values := map[string]inboxtext.Value{}
	if len(keys) > 0 {
		var err error
		values, err = a.inboxMessageValues(db, c, account, contact, actor, keys, deferred...)
		if err != nil {
			return err
		}
	}
	renderComponent := func(content, component string, authored map[string]string) (string, map[string]string, error) {
		names := templateutil.ExtParamNames(content)
		if len(names) > 32 || len(authored) != len(names) {
			return content, nil, inboxEligibilityError{"template_parameters_missing"}
		}
		params := map[string]string{}
		for _, name := range names {
			text, present := authored[name]
			if !present || !validInboxParameterName(name) {
				return content, nil, inboxEligibilityError{"template_parameters_missing"}
			}
			if action.Personalise {
				result, err := inboxtext.Render(text, values)
				if errors.Is(err, inboxtext.ErrInvalid) {
					return content, nil, inboxEligibilityError{"variables_invalid"}
				}
				if err != nil {
					return content, nil, err
				}
				intent.Missing = append(intent.Missing, result.Missing...)
				text = result.Text
			}
			if strings.TrimSpace(text) == "" {
				return content, nil, inboxEligibilityError{"template_parameters_missing"}
			}
			params[name] = text
		}
		// A single pass prevents parameter values containing braces from becoming
		// another template expression or replacing another parameter.
		rendered := templateutil.ParameterPattern.ReplaceAllStringFunc(content, func(token string) string {
			name := strings.TrimSpace(token[2 : len(token)-2])
			return params[name]
		})
		limit := inboxtext.MaxCharacters
		if component == "header" {
			limit = 60
		}
		if utf8.RuneCountInString(rendered) > limit {
			return content, nil, inboxEligibilityError{"variables_invalid"}
		}
		return rendered, params, nil
	}
	var err error
	intent.Text, intent.BodyParameters, err = renderComponent(t.BodyContent, "body", action.BodyParameters)
	if err != nil {
		return err
	}
	intent.Header, intent.HeaderParameters, err = renderComponent(t.HeaderContent, "header", action.HeaderParameters)
	if err != nil {
		return err
	}
	if len(intent.Missing) > 0 {
		return inboxEligibilityError{"variables_missing"}
	}
	return nil
}
