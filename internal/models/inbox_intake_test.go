package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInboxFieldIntakeEmailAndHTTPSAreExplicitTypes(t *testing.T) {
	for kind, valid := range map[string][]string{"email": {"person@example.invalid", "name+demo@example.invalid", "", "  "}, "url": {"https://example.invalid/path?reference=synthetic#part", "HTTPS://example.invalid/", "https://example.invalid:8443/", ""}} {
		d := InboxFieldDefinition{Scope: "contact", Key: "example", Label: "Example", Kind: kind}
		require.NoError(t, d.Validate())
		for _, value := range valid {
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			require.NoError(t, d.ValidateValue(raw), kind+":"+value)
		}
		require.NoError(t, d.ValidateValue(json.RawMessage("null")))
		require.Error(t, d.ValidateValue(json.RawMessage("42")))
	}
	for kind, values := range map[string][]string{"email": {"Person <person@example.invalid>", "not-an-email", " person@example.invalid", strings.Repeat("a", 321) + "@example.invalid"}, "url": {"http://example.invalid/", "javascript:alert(1)", "file:///tmp/example", "/relative", "https://user:password@example.invalid/", " https://example.invalid/"}} {
		d := InboxFieldDefinition{Kind: kind}
		for _, value := range values {
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			require.Error(t, d.ValidateValue(raw), kind+":"+value)
		}
	}
	legacy := InboxFieldDefinition{Kind: "text"}
	require.NoError(t, legacy.ValidateValue(json.RawMessage(`"existing text is not an email"`)), "legacy text is never reinterpreted")
}

func TestInboxFieldConditionalRequirementShapeIsBoundedAndOptional(t *testing.T) {
	id := uuid.New()
	base := InboxFieldDefinition{Scope: "case", Key: "reference", Label: "Reference", Kind: "text", RequiredOnResolve: true}
	require.NoError(t, base.Validate())
	base.RequiredWhen = &InboxFieldRequirement{AccountID: &id}
	require.Error(t, base.Validate(), "unconditional and conditional are explicit alternatives")
	base.RequiredOnResolve = false
	require.NoError(t, base.Validate())
	for _, rule := range []InboxFieldRequirement{
		{}, {Match: "nested", AccountID: &id}, {Predicates: []InboxPredicate{{Field: "message_contains"}}},
		{Predicates: make([]InboxPredicate, 21)}, {AccountID: new(uuid.UUID)},
		{Predicates: []InboxPredicate{{Field: "custom_field"}}},
		{Predicates: []InboxPredicate{{Field: "priority", DefinitionID: &id}}},
	} {
		require.Error(t, rule.Validate())
	}
	for _, value := range []string{"false", "0"} {
		rule := InboxFieldRequirement{Match: "any", Predicates: []InboxPredicate{{Field: "custom_field", DefinitionID: &id, Value: json.RawMessage(value)}}}
		require.NoError(t, rule.Validate())
	}
	raw, err := json.Marshal(base)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "required_when", "raw models cannot expose author-only condition values")
}
