package handlers_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
)

func duplicateRequest(t *testing.T, orgID, userID uuid.UUID, id string) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, orgID, userID)
	r.RequestCtx.SetUserValue("id", id)
	return r
}

func duplicateFixture(t *testing.T) (*handlers.App, *models.Organization, *models.User, *models.ChatbotFlow) {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRole(t, app.DB, org.ID, "copier", getChatbotFlowPermissions(t, app))
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	flow := createTestChatbotFlow(t, app, org.ID, "Welcome")
	return app, org, user, flow
}

func TestApp_DuplicateChatbotFlow_PreservesConfigurationWithoutActivation(t *testing.T) {
	for _, channel := range []string{"whatsapp", "sms"} {
		t.Run(channel, func(t *testing.T) {
			app, org, user, source := duplicateFixture(t)
			source.Channel = channel
			source.WhatsAppAccount = "fixture-account"
			source.TriggerButtonID = "launch"
			source.InitialMessage = "Welcome"
			source.InitialMessageType = models.FlowStepType("text")
			source.CompletionMessage = "Complete"
			source.OnCompleteAction = "webhook"
			source.CompletionConfig = models.JSONB{"url": "https://fixture.invalid", "headers": map[string]any{"X-Test": "fixture-only"}}
			source.TimeoutMessage = "Timeout"
			source.CancelKeywords = models.StringArray{"cancel", "stop"}
			source.PanelConfig = models.JSONB{"sections": []any{"contact", "notes"}}
			source.Graph = models.JSONB{"version": 2, "entry_node": "welcome", "nodes": []any{map[string]any{"id": "welcome", "type": "text", "data": map[string]any{"message": "Hello"}}}, "edges": []any{}}
			require.NoError(t, app.DB.Save(source).Error)
			var before models.ChatbotFlow
			require.NoError(t, app.DB.First(&before, "id = ?", source.ID).Error)
			r := duplicateRequest(t, org.ID, user.ID, source.ID.String())
			require.NoError(t, app.DuplicateChatbotFlow(r))
			require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
			var response struct {
				Data struct {
					ID uuid.UUID `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(testutil.GetResponseBody(r), &response))
			require.NotEqual(t, source.ID, response.Data.ID)
			var copy models.ChatbotFlow
			require.NoError(t, app.DB.First(&copy, "id = ?", response.Data.ID).Error)
			assert.False(t, copy.IsEnabled, "GORM default:true must not activate a new copy")
			assert.Equal(t, "Welcome (Copy)", copy.Name)
			assert.Equal(t, &user.ID, copy.CreatedByID)
			assert.Equal(t, &user.ID, copy.UpdatedByID)
			assert.False(t, copy.CreatedAt.Before(before.CreatedAt))
			// Compare every configuration field, including fields absent from Create's DTO.
			copy.BaseModel = before.BaseModel
			copy.Name, copy.IsEnabled = before.Name, before.IsEnabled
			copy.CreatedByID, copy.UpdatedByID = before.CreatedByID, before.UpdatedByID
			assert.Equal(t, before, copy)
			var after models.ChatbotFlow
			require.NoError(t, app.DB.First(&after, "id = ?", source.ID).Error)
			assert.Equal(t, before, after, "copy must not mutate the source")
			require.Eventually(t, func() bool {
				var audit models.AuditLog
				if app.DB.Where("resource_id = ? AND user_id = ?", response.Data.ID, user.ID).First(&audit).Error != nil {
					return false
				}
				data, err := json.Marshal(audit.Changes)
				return err == nil && strings.Contains(string(data), source.ID.String()) && !strings.Contains(string(data), "fixture-only")
			}, time.Second, 10*time.Millisecond, "audit records the actor and source without private configuration")
			for _, table := range []string{"chatbot_sessions", "messages", "channel_attempts", "channel_bot_jobs"} {
				var count int64
				require.NoError(t, app.DB.Table(table).Where("organization_id = ?", org.ID).Count(&count).Error)
				assert.Zero(t, count, "copy must not start sessions or dispatch messages: %s", table)
			}
		})
	}
}

func TestApp_DuplicateChatbotFlow_RollsBackWhenDisablingFails(t *testing.T) {
	app, org, user, source := duplicateFixture(t)
	name := "rollback-" + uuid.NewString()
	require.NoError(t, app.DB.Model(source).Update("name", name).Error)
	constraint := "duplicate_fixture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// Insert's default=true passes; the explicit disable must fail and roll back
	// the entire insert. The constraint applies only to this unique fixture name.
	require.NoError(t, app.DB.Exec("ALTER TABLE chatbot_flows ADD CONSTRAINT "+constraint+" CHECK (name <> '"+name+" (Copy)' OR is_enabled = true)").Error)
	t.Cleanup(func() { app.DB.Exec("ALTER TABLE chatbot_flows DROP CONSTRAINT " + constraint) })
	r := duplicateRequest(t, org.ID, user.ID, source.ID.String())
	require.NoError(t, app.DuplicateChatbotFlow(r))
	assert.Equal(t, 500, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, app.DB.Model(&models.ChatbotFlow{}).Where("organization_id = ?", org.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "failed disabling must not leave an enabled copy")
}

func TestApp_DuplicateChatbotFlow_PermissionsAndIsolation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions []string
		status      int
	}{
		{"read only", []string{"flows.chatbot:read"}, 403},
		{"write only", []string{"flows.chatbot:write"}, 403},
		{"no permissions", nil, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, org, _, source := duplicateFixture(t)
			role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "restricted", tc.permissions)
			user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
			r := duplicateRequest(t, org.ID, user.ID, source.ID.String())
			require.NoError(t, app.DuplicateChatbotFlow(r))
			assert.Equal(t, tc.status, testutil.GetResponseStatusCode(r))
			var count int64
			app.DB.Model(&models.ChatbotFlow{}).Where("organization_id = ?", org.ID).Count(&count)
			assert.Equal(t, int64(1), count)
		})
	}
	app, org, user, _ := duplicateFixture(t)
	other := testutil.CreateTestOrganization(t, app.DB)
	foreign := createTestChatbotFlow(t, app, other.ID, "Private")
	for _, tc := range []struct {
		id     string
		status int
	}{{foreign.ID.String(), 404}, {uuid.NewString(), 404}, {"bad-id", 400}} {
		r := duplicateRequest(t, org.ID, user.ID, tc.id)
		require.NoError(t, app.DuplicateChatbotFlow(r))
		assert.Equal(t, tc.status, testutil.GetResponseStatusCode(r))
	}
	r := testutil.NewJSONRequest(t, map[string]any{})
	require.NoError(t, app.DuplicateChatbotFlow(r))
	assert.Equal(t, 401, testutil.GetResponseStatusCode(r))
}

func TestApp_DuplicateChatbotFlow_ConcurrentDisabledCopiesAndLongNames(t *testing.T) {
	app, org, user, source := duplicateFixture(t)
	require.NoError(t, app.DB.Model(source).Updates(map[string]any{"name": strings.Repeat("界", 255), "is_enabled": false}).Error)
	// Separate authenticated requests exercise concurrent transactions on the same source.
	requests := make([]*fastglue.Request, 6)
	for i := range requests {
		requests[i] = duplicateRequest(t, org.ID, user.ID, source.ID.String())
	}
	var wg sync.WaitGroup
	for _, r := range requests {
		wg.Add(1)
		go func(r *fastglue.Request) { defer wg.Done(); _ = app.DuplicateChatbotFlow(r) }(r)
	}
	wg.Wait()
	for _, r := range requests {
		require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	}
	var copies []models.ChatbotFlow
	require.NoError(t, app.DB.Where("organization_id = ? AND id <> ?", org.ID, source.ID).Find(&copies).Error)
	require.Len(t, copies, 6)
	for _, copy := range copies {
		assert.False(t, copy.IsEnabled)
		assert.LessOrEqual(t, len([]rune(copy.Name)), 255)
		assert.True(t, strings.HasSuffix(copy.Name, " (Copy)"))
	}
}
