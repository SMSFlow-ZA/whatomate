package inboxlive

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDecodeIdentifierOnlyInvalidations(t *testing.T) {
	account := uuid.New()
	original := Event{Protocol: 1, OrganizationID: uuid.New(), Kind: "case", ResourceID: uuid.New(), Version: 42, After: &Scope{AccountID: &account}}
	valid, err := json.Marshal(original)
	require.NoError(t, err)
	value, err := Decode(valid)
	require.NoError(t, err)
	require.Equal(t, original, value)
	for _, payload := range []string{
		"", strings.Repeat(" ", 1025), string(valid) + `{}`,
		strings.Replace(string(valid), `"v":1`, `"v":2`, 1),
		strings.Replace(string(valid), `"version":42`, `"version":0`, 1),
		strings.Replace(string(valid), `"kind":"case"`, `"kind":"arbitrary"`, 1),
		strings.Replace(string(valid), account.String(), uuid.Nil.String(), 1),
		strings.Replace(string(valid), original.OrganizationID.String(), uuid.Nil.String(), 1),
		strings.Replace(string(valid), `"v":1`, `"prompt":"private","v":1`, 1),
		strings.Replace(string(valid), `"account_id":`, `"email":"private","account_id":`, 1),
	} {
		_, err := Decode([]byte(payload))
		require.ErrorIs(t, err, ErrInvalidEvent)
		require.NotContains(t, err.Error(), "private")
	}
}
