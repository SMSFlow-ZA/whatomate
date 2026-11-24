package inboxtext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderTypedValuesAndLiteralSubstitution(t *testing.T) {
	got, err := Render(`Hi {{contact_name}}, {{case.field.balance}} / {{case.field.accepted}} {literal}.`, map[string]Value{
		"contact_name": {Text: `Zoë {{phone_number}}`}, "case.field.balance": {Text: "0"}, "case.field.accepted": {Text: "false"},
	})
	require.NoError(t, err)
	require.Empty(t, got.Missing)
	require.Equal(t, `Hi Zoë {{phone_number}}, 0 / false {literal}.`, got.Text)
}

func TestRenderMissingAndExplicitFallback(t *testing.T) {
	got, err := Render(`{{contact_name | "customer"}} {{case.field.old}} {{case.field.old}} {{phone_number}}`, map[string]Value{
		"contact_name": {}, "case.field.old": {Reason: "archived"}, "phone_number": {Reason: "unavailable"},
	})
	require.NoError(t, err)
	require.Equal(t, `customer {{case.field.old}} {{case.field.old}} {{phone_number}}`, got.Text)
	require.Equal(t, []Missing{{Key: "case.field.old", Reason: "archived"}, {Key: "phone_number", Reason: "unavailable"}}, got.Missing)
	unknown, err := Render(`{{case.field.unknown | "fallback"}}`, nil)
	require.NoError(t, err)
	require.Len(t, unknown.Missing, 1)
}

func TestRenderFallbackIsData(t *testing.T) {
	got, err := Render(`{{contact_name | "{{not_code}} | \\"}}`, map[string]Value{"contact_name": {}})
	require.NoError(t, err)
	require.Equal(t, "{{not_code}} | \\", got.Text)
	keys, err := Keys(`{{contact_name}} {{case.field.ticket}} {{contact_name}}`)
	require.NoError(t, err)
	require.Equal(t, []string{"contact_name", "case.field.ticket"}, keys)
}

func TestRenderRejectsExecutableOrUnboundedExpressions(t *testing.T) {
	for _, text := range []string{
		`{{access_token}}`, `{{contact.email}}`, `{{case.field.__proto__}}`, `{{case.field.x.y}}`,
		`{{contact_name | null}}`, `{{contact_name | 42}}`, `{{contact_name | unquoted}}`, `{{contact_name`,
		strings.Repeat("{{case_id}}", 65), strings.Repeat("é", 4097),
	} {
		t.Run(text[:min(60, len(text))], func(t *testing.T) {
			_, err := Render(text, nil)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	_, err := Render("{{contact_name}}{{contact_name}}", map[string]Value{"contact_name": {Text: strings.Repeat("x", 2500)}})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestKeysLimitsTheEntireMessageRatherThanEachParameter(t *testing.T) {
	_, err := KeysForTexts([]string{strings.Repeat("{{case_id}}", 40), strings.Repeat("{{contact_name}}", 25)})
	require.ErrorIs(t, err, ErrInvalid)
	keys, err := KeysForTexts([]string{strings.Repeat("{{case_id}}", 32), strings.Repeat("{{contact_name}}", 32)})
	require.NoError(t, err)
	require.Equal(t, []string{"case_id", "contact_name"}, keys)
}
