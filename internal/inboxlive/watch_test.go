package inboxlive

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInboxWatchValidatesBoundedClientContextWithoutAuthorityClaims(t *testing.T) {
	account, team, contact, caseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, scope := range []WatchScope{
		{Kind: "queue"}, {Kind: "queue", AccountID: &account, TeamID: &team},
		{Kind: "case", CaseID: &caseID},
		{Kind: "conversation", AccountID: &account, ContactID: &contact},
		{Kind: "contact", ContactID: &contact},
		{Kind: "workload", AccountID: &account}, {Kind: "supervisor", TeamID: &team},
		{Kind: "capacity", AccountID: &account, TeamID: &team},
	} {
		data, err := json.Marshal(WatchSet{Protocol: 1, Generation: 1, Scopes: []WatchScope{scope}})
		require.NoError(t, err)
		set, err := DecodeWatch(data)
		require.NoError(t, err)
		require.Equal(t, scope.Key(), set.Scopes[0].Key())
	}
	for _, scope := range []WatchScope{
		{Kind: "all_organisations"}, {Kind: "case"}, {Kind: "conversation", ContactID: &contact},
		{Kind: "case", CaseID: &caseID, AccountID: &account},
		{Kind: "contact", ContactID: &contact, TeamID: &team},
		{Kind: "capacity", TeamID: &team}, {Kind: "queue", CaseID: &caseID},
		{Kind: "workload", ContactID: &contact}, {Kind: "supervisor", ContactID: &contact},
	} {
		require.ErrorIs(t, scope.Validate(), ErrInvalidWatch)
	}
	for _, payload := range []string{
		"", strings.Repeat(" ", 513), `{"v":2,"generation":1,"scopes":[]}`,
		`{"v":1,"generation":0,"scopes":[]}`, `{"v":1,"generation":4294967296,"scopes":[]}`,
		`{"v":1,"generation":1,"is_super_admin":true,"scopes":[]}`,
		`{"v":1,"generation":1,"scopes":[{"kind":"queue","organisation_id":"` + account.String() + `"}]}`,
		`{"v":1,"generation":1,"scopes":[{"kind":"queue","account_id":"` + uuid.Nil.String() + `"}]}`,
		`{"v":1,"generation":1,"scopes":[{"kind":"queue"},{"kind":"queue"}]}`,
		`{"v":1,"generation":1,"scopes":[]} {}`,
	} {
		_, err := DecodeWatch([]byte(payload))
		require.ErrorIs(t, err, ErrInvalidWatch)
	}
	set, err := DecodeWatch([]byte(`{"v":1,"generation":2,"scopes":[]}`))
	require.NoError(t, err)
	require.Empty(t, set.Scopes)
}

func TestInboxWatchThreeFullScopesFitExistingWebSocketFrame(t *testing.T) {
	set := WatchSet{Protocol: 1, Generation: ^uint32(0)}
	for _, kind := range []string{"supervisor", "conversation", "workload"} {
		account, second := uuid.New(), uuid.New()
		scope := WatchScope{Kind: kind, AccountID: &account, TeamID: &second}
		if kind == "conversation" {
			scope.TeamID, scope.ContactID = nil, &second
		}
		set.Scopes = append(set.Scopes, scope)
	}
	frame, err := json.Marshal(struct {
		Type    string   `json:"type"`
		Payload WatchSet `json:"payload"`
	}{Type: "inbox_watch", Payload: set})
	require.NoError(t, err)
	require.LessOrEqual(t, len(frame), 512, "preserve the current authenticated frame limit")
	data, err := json.Marshal(set)
	require.NoError(t, err)
	_, err = DecodeWatch(data)
	require.NoError(t, err)
	set.Scopes = append(set.Scopes, WatchScope{Kind: "queue"})
	data, err = json.Marshal(set)
	require.NoError(t, err)
	_, err = DecodeWatch(data)
	require.ErrorIs(t, err, ErrInvalidWatch)
}
