package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestDuplicateChatbotFlow_InvalidatesCacheButNeverAddsActiveCopy(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	source := models.ChatbotFlow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Channel: "whatsapp", Name: "Active source", IsEnabled: true}
	require.NoError(t, app.DB.Create(&source).Error)
	_, err := app.getChatbotFlowsCached(org.ID)
	require.NoError(t, err)
	key := fmt.Sprintf("%s%s", flowsCachePrefix, org.ID)
	t.Cleanup(func() { app.InvalidateChatbotFlowsCache(org.ID) })
	require.Equal(t, int64(1), app.Redis.Exists(context.Background(), key).Val())
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, org.ID, user.ID)
	r.RequestCtx.SetUserValue("id", source.ID.String())
	require.NoError(t, app.DuplicateChatbotFlow(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.Equal(t, int64(0), app.Redis.Exists(context.Background(), key).Val())
	flows, err := app.getChatbotFlowsCached(org.ID)
	require.NoError(t, err)
	require.Len(t, flows, 1)
	require.Equal(t, source.ID, flows[0].ID)
	var response struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(r), &response))
	_, err = app.getChatbotFlowByIDCached(org.ID, response.Data.ID)
	require.Error(t, err)
}
