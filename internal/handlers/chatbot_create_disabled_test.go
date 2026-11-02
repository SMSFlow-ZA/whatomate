package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestApp_CreateChatbotFlow_PreservesExplicitActivation(t *testing.T) {
	for _, channel := range []string{"whatsapp", "sms"} {
		for _, state := range []string{"disabled", "enabled", "omitted"} {
			t.Run(channel+"/"+state, func(t *testing.T) {
				app := newTestApp(t)
				org := testutil.CreateTestOrganization(t, app.DB)
				role := testutil.CreateTestRole(t, app.DB, org.ID, "draft-builder", getChatbotFlowPermissions(t, app))
				user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
				body := map[string]any{"name": "Activation regression", "channel": channel,
					"graph": map[string]any{"version": 2, "entry_node": "done", "nodes": []any{
						map[string]any{"id": "done", "type": "end", "config": map[string]any{"message": "Synthetic fixture"}},
					}, "edges": []any{}}}
				if state != "omitted" {
					body["enabled"] = state == "enabled"
				}
				r := testutil.NewJSONRequest(t, body)
				testutil.SetAuthContext(r, org.ID, user.ID)
				require.NoError(t, app.CreateChatbotFlow(r))
				require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
				var response struct {
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(testutil.GetResponseBody(r), &response))
				var saved models.ChatbotFlow
				require.NoError(t, app.DB.First(&saved, "id = ?", response.Data.ID).Error)
				require.Equal(t, state == "enabled", saved.IsEnabled, "false and omitted must never activate a draft")
			})
		}
	}
}

func TestApp_CreateChatbotFlow_RollsBackWhenActivationWriteFails(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRole(t, app.DB, org.ID, "draft-builder", getChatbotFlowPermissions(t, app))
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	name := "create-" + uuid.NewString()
	constraint := "create_fixture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, app.DB.Exec("ALTER TABLE chatbot_flows ADD CONSTRAINT "+constraint+" CHECK (name <> '"+name+"' OR is_enabled = true)").Error)
	t.Cleanup(func() { app.DB.Exec("ALTER TABLE chatbot_flows DROP CONSTRAINT " + constraint) })
	r := testutil.NewJSONRequest(t, map[string]any{"name": name, "enabled": false})
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, app.CreateChatbotFlow(r))
	require.Equal(t, 500, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, app.DB.Model(&models.ChatbotFlow{}).Where("organization_id = ?", org.ID).Count(&count).Error)
	require.Zero(t, count, "failed explicit disable must never leave an active flow")
}
