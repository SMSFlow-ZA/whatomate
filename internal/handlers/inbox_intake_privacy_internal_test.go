package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/logf"
)

func TestInboxIntakeFlowReplyNeverLogsRawOrParsedAnswers(t *testing.T) {
	var logs auditLockedLog
	app := &App{Log: logf.New(logf.Opts{Writer: &logs, Level: logf.DebugLevel})}
	for _, valid := range []bool{true, false} {
		answer := `{"email":"synthetic-private@example.invalid","token":"synthetic-intake-secret"}`
		if !valid {
			answer += `invalid-synthetic-answer`
		}
		body, err := json.Marshal(map[string]any{"type": "interactive", "interactive": map[string]any{"type": "nfm_reply", "nfm_reply": map[string]any{"body": "Synthetic response", "response_json": answer}}})
		require.NoError(t, err)
		var incoming IncomingTextMessage
		require.NoError(t, json.Unmarshal(body, &incoming))
		extracted := app.extractMessageContent(context.Background(), incoming, &models.WhatsAppAccount{})
		require.Equal(t, "nfm_reply", extracted.Type)
		require.Equal(t, "Synthetic response", extracted.Text)
		if valid {
			require.Equal(t, "synthetic-intake-secret", extracted.FlowResponseData["token"])
		} else {
			require.Nil(t, extracted.FlowResponseData)
		}
	}
	require.Contains(t, logs.text(), "invalid_flow_response")
	require.NotContains(t, logs.text(), "synthetic-private")
	require.NotContains(t, logs.text(), "synthetic-intake-secret")
	require.NotContains(t, logs.text(), "invalid-synthetic-answer")
	require.NotContains(t, logs.text(), "response_json")
}
