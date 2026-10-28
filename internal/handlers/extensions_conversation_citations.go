package handlers

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
)

const extensionConversationReplyBytes = 4096

var extensionCitationDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func extensionCitationLabel(value string, maximum int) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) && r != ' ' {
			return false
		}
	}
	return true
}

// Render only provenance returned by the authorized current-source service.
// Source has no title field: do not infer a title or leak unrelated page text.
// Keep digest references in structured evidence, not the human-facing label.
// Never clip an approved quote or silently omit citations to fit the channel.
func extensionConversationAnswer(result ai.Result) conversation.Answer {
	fallback := conversation.Answer{Text: ai.FixedResponse, Handoff: true}
	if result.Handoff || strings.TrimSpace(result.Text) == "" || !utf8.ValidString(result.Text) || len(result.Text) > extensionConversationReplyBytes || len(result.Citations) == 0 || len(result.Citations) > 20 {
		return fallback
	}
	seen := map[string]knowledge.Source{}
	references := []string{}
	lines := []string{}
	supported := false
	for _, source := range result.Citations {
		if !extensionCitationLabel(source.DocumentID, 200) || !extensionCitationLabel(source.Version, 100) || !extensionCitationDigest.MatchString(source.ContentHash) || !extensionCitationDigest.MatchString(source.ManifestHash) || source.Page.Number < 1 || source.Page.Text == "" || !utf8.ValidString(source.Page.Text) || source.Reference != fmt.Sprintf("%s:%d", source.ManifestHash, source.Page.Number) {
			return fallback
		}
		if previous, ok := seen[source.Reference]; ok {
			if previous != source {
				return fallback
			}
			continue
		}
		seen[source.Reference] = source
		references = append(references, source.Reference)
		lines = append(lines, fmt.Sprintf("- %s | version %s | page %d", source.DocumentID, source.Version, source.Page.Number))
		if strings.Contains(source.Page.Text, result.Text) {
			supported = true
		}
	}
	if !supported {
		return fallback
	}
	text := result.Text + "\n\nSources:\n" + strings.Join(lines, "\n")
	if len(text) > extensionConversationReplyBytes {
		return fallback
	}
	return conversation.Answer{Text: text, Citations: references}
}
