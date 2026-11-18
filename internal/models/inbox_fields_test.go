package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboxFieldValueTypes(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		options []string
		valid   string
		invalid string
	}{
		{"text", nil, `"Case reference"`, `42`},
		{"number", nil, `0`, `"0"`},
		{"date", nil, `"2028-02-29"`, `"2026-02-29"`},
		{"boolean", nil, `false`, `"false"`},
		{"select", []string{"Billing", "Technical"}, `"Billing"`, `"billing"`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := InboxFieldDefinition{Scope: "case", Key: "example_field", Label: "Example field", Kind: tc.kind, Options: tc.options}
			require.NoError(t, f.Validate())
			require.NoError(t, f.ValidateValue(json.RawMessage(tc.valid)))
			require.NoError(t, f.ValidateValue(json.RawMessage("null")))
			require.Error(t, f.ValidateValue(json.RawMessage(tc.invalid)))
		})
	}
}

func TestInboxFieldDefinitionRejectsDuplicateOptions(t *testing.T) {
	f := InboxFieldDefinition{Scope: "contact", Key: "tier", Label: "Tier", Kind: "select", Options: []string{"Standard", "Standard"}}
	require.Error(t, f.Validate())
	f.Options = []string{"Standard", "Priority"}
	require.NoError(t, f.Validate())
	f.Key = "Tier"
	require.Error(t, f.Validate())
}
