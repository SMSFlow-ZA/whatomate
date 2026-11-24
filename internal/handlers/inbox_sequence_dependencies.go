package handlers

import (
	"sort"

	"github.com/shridarpatil/whatomate/internal/inboxtext"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Dependencies use the existing field variable grammar. There is no expression
// evaluator, raw response lookup or cross-run data source.
func inboxDeferredDependencies(texts []string, pending ...map[string]bool) ([]string, error) {
	keys, err := inboxtext.KeysForTexts(texts)
	if err != nil || len(pending) > 1 {
		return nil, inboxtext.ErrInvalid
	}
	result := []string{}
	if len(pending) == 1 {
		for _, key := range keys {
			if pending[0][key] {
				result = append(result, key)
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func inboxActionPersonalisationTexts(action models.InboxAction) []string {
	if !action.Personalise {
		return nil
	}
	texts := []string{}
	if action.Type == "send_text" {
		texts = append(texts, action.Value)
	}
	for _, parameters := range []map[string]string{action.BodyParameters, action.HeaderParameters} {
		keys := make([]string, 0, len(parameters))
		for name := range parameters {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			texts = append(texts, parameters[name])
		}
	}
	return texts
}

func inboxSequenceDeferredActions(preview inboxWorkflowPreview) map[int]bool {
	result := map[int]bool{}
	for index, keys := range preview.DeferredActions {
		if len(keys) != 0 {
			result[index] = true
		}
	}
	return result
}
