package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/aiconnections"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/aiproviders"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

const aiFixturePolicy = `[{"id":"fixture","kind":"compatible","base_url":"https://fixture.example.test/v1"}]`

type chatbotAIProvider func(aiproviders.Request) (aiproviders.Reply, error)

func (f chatbotAIProvider) Generate(_ context.Context, r aiproviders.Request) (aiproviders.Reply, error) {
	return f(r)
}

func TestConnectedAIThroughBothChatbotPaths(t *testing.T) {
	for _, path := range []string{"fallback", "flow"} {
		t.Run(path, func(t *testing.T) {
			app, org, account, contact, session := newGraphTestFixtures(t)
			app.Config = &config.Config{}
			createChatbotSettings(t, app, org.ID, account.Name, models.AIConfig{Enabled: true, IncludeHistory: true, SystemPrompt: "Synthetic instructions"})
			require.NoError(t, app.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ?", org.ID).Update("fallback_message", "Ordinary fallback").Error)
			ctx := context.Background()
			calls := 0
			provider := chatbotAIProvider(func(r aiproviders.Request) (aiproviders.Reply, error) {
				calls++
				if calls == 1 {
					return aiproviders.Reply{Text: "READY"}, nil
				}
				if calls == 3 {
					return aiproviders.Reply{}, aiproviders.ErrRateLimited
				}
				require.Equal(t, []aiproviders.Message{
					{Role: "system", Content: "Synthetic instructions"}, {Role: "user", Content: "Synthetic customer text"},
				}, r.Messages)
				return aiproviders.Reply{Text: "Synthetic model reply"}, nil
			})
			s, err := aiconnections.New(app.DB, strings.Repeat("k", 32), true, aiFixturePolicy, func(aiproviders.Target) aiproviders.Provider { return provider })
			require.NoError(t, err)
			app.AIConnections = s
			profile, err := s.Create(ctx, org.ID, "Fixture", "fixture", "fixture-model", "fixture-token")
			require.NoError(t, err)
			_, err = s.Probe(ctx, org.ID, profile.ID)
			require.NoError(t, err)
			require.NoError(t, s.Assign(ctx, org.ID, account.ID, profile.ID, false, false))
			if path == "flow" {
				flow := newAIResponseFlow(t, app, org, account, "Synthetic customer text")
				require.NoError(t, app.runChatGraph(account, contact, session, flow, "Synthetic customer text", "", nil))
				require.NoError(t, app.DB.First(session, session.ID).Error)
				require.Equal(t, models.SessionStatusCompleted, session.Status)
			} else {
				msg := IncomingTextMessage{From: contact.PhoneNumber, ID: "wamid." + uuid.NewString(), Type: "text"}
				msg.Text = &struct {
					Body string `json:"body"`
				}{Body: "Synthetic customer text"}
				app.processIncomingMessageFull(account.PhoneID, msg, "Synthetic")
				msg.ID = "wamid." + uuid.NewString()
				app.processIncomingMessageFull(account.PhoneID, msg, "Synthetic")
				var fallback int64
				require.NoError(t, app.DB.Model(&models.Message{}).Where("contact_id = ? AND content = ?", contact.ID, "Ordinary fallback").Count(&fallback).Error)
				require.EqualValues(t, 1, fallback)
			}
			var sent int64
			require.NoError(t, app.DB.Model(&models.Message{}).Where("contact_id = ? AND content = ?", contact.ID, "Synthetic model reply").Count(&sent).Error)
			require.EqualValues(t, 1, sent)
		})
	}
}

func TestAIConnectionsPermissionAndSecretProjection(t *testing.T) {
	app := newProcessorTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	limited := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "Read only AI fixture", []string{"settings.chatbot:read"})
	reader := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&limited.ID))
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	s, err := aiconnections.New(app.DB, strings.Repeat("k", 32), true, aiFixturePolicy, nil)
	require.NoError(t, err)
	app.AIConnections = s
	payload := map[string]string{"name": "Fixture", "endpoint_id": "fixture", "model": "fixture-model", "credential": "write-only-fixture-key"}
	req := testutil.NewJSONRequest(t, payload)
	testutil.SetAuthContext(req, org.ID, reader.ID)
	require.NoError(t, app.CreateAIConnection(req))
	require.Equal(t, 403, testutil.GetResponseStatusCode(req))
	req = testutil.NewJSONRequest(t, payload)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	require.NoError(t, app.CreateAIConnection(req))
	require.Equal(t, 200, testutil.GetResponseStatusCode(req))
	require.NotContains(t, string(testutil.GetResponseBody(req)), "write-only-fixture-key")
	req = testutil.NewJSONRequest(t, map[string]string{})
	testutil.SetAuthContext(req, org.ID, reader.ID)
	require.NoError(t, app.ListAIConnections(req))
	require.Equal(t, 200, testutil.GetResponseStatusCode(req))
	require.NotContains(t, string(testutil.GetResponseBody(req)), "write-only-fixture-key")
	require.Contains(t, string(testutil.GetResponseBody(req)), `"can_write":false`)
}
