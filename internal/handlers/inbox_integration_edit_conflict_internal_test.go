package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func integrationReviewActor(t *testing.T, f integrationFixture) models.CustomRole {
	t.Helper()
	f.a.Redis = testutil.SetupTestRedis(t)
	if f.a.Redis == nil {
		t.Skip("TEST_REDIS_URL required")
	}
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "integration-current-authority", []string{"chat:read", "chat:write", "contacts:read", "contacts:write", models.ResourceCustomActions + ":write", "settings.general:write"})
	require.NoError(t, f.a.DB.Model(f.actor).Updates(map[string]any{"is_super_admin": false, "role_id": role.ID}).Error)
	require.NoError(t, f.a.DB.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.c.OrganizationID, f.actor.ID).Update("role_id", role.ID).Error)
	return *role
}

func TestInboxIntegrationOutputHoldsRevokedActorEvenWithWarmCache(t *testing.T) {
	f := newIntegrationFixture(t)
	role := integrationReviewActor(t, f)
	require.True(t, f.a.HasPermission(f.actor.ID, models.ResourceCustomActions, models.ActionWrite, f.c.OrganizationID))
	field := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "provider_reference", Label: "Provider reference", Kind: "text"}
	require.NoError(t, f.a.DB.Create(&field).Error)
	f.d.Outputs = []models.InboxIntegrationOutput{{Name: "reference", DefinitionID: field.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	execution := f.enqueue(t)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		require.NoError(t, f.a.DB.Exec("DELETE FROM role_permissions rp USING permissions p WHERE rp.permission_id=p.id AND rp.custom_role_id=? AND p.resource=? AND p.action=?", role.ID, models.ResourceCustomActions, models.ActionWrite).Error)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"synthetic-provider-reference"`)}}, nil
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	require.NoError(t, f.a.DB.First(&execution, "id=?", execution.ID).Error)
	require.Equal(t, "succeeded", execution.State, "the genuine fake-provider receipt is retained")
	require.Nil(t, execution.OutputsAppliedAt)
	require.Equal(t, "output_context_changed", execution.Outcome)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("definition_id=?", field.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntegrationQueuedRuleChecksCurrentSettingsPermission(t *testing.T) {
	f := newIntegrationFixture(t)
	role := integrationReviewActor(t, f)
	now := time.Now().UTC()
	f.w.Kind, f.w.EnabledAt = "rule", &now
	require.NoError(t, f.a.DB.Save(&f.w).Error)
	require.NoError(t, f.a.DB.First(&f.w, "id=?", f.w.ID).Error)
	f.run.WorkflowUpdatedAt = f.w.UpdatedAt
	require.NoError(t, f.a.DB.Model(&f.run).Update("workflow_updated_at", f.w.UpdatedAt).Error)
	require.True(t, f.a.HasPermission(f.actor.ID, models.ResourceSettingsGeneral, models.ActionWrite, f.c.OrganizationID))
	execution := f.enqueue(t)
	require.NoError(t, f.a.DB.Exec("DELETE FROM role_permissions rp USING permissions p WHERE rp.permission_id=p.id AND rp.custom_role_id=? AND p.resource=? AND p.action=?", role.ID, models.ResourceSettingsGeneral, models.ActionWrite).Error)
	calls := 0
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls++
		return inboxintegration.Response{}, nil
	})
	require.NoError(t, f.a.dispatchInboxIntegration(context.Background(), execution.ID))
	require.NoError(t, f.a.DB.First(&execution, "id=?", execution.ID).Error)
	require.Equal(t, "blocked", execution.State)
	require.Equal(t, "permission_required", execution.Outcome)
	require.Zero(t, calls)
}

func TestInboxIntegrationStaleEditCannotUndoDisableOrCredentialRotation(t *testing.T) {
	f := newIntegrationFixture(t)
	stale := map[string]any{"name": f.d.Name, "description": "Stale description edit", "endpoint_key": f.d.EndpointKey, "auth_mode": f.d.AuthMode, "enabled": true, "inputs": f.d.Inputs, "outputs": f.d.Outputs, "reference_key": f.d.ReferenceKey, "updated_at": f.d.UpdatedAt}
	save := func(body map[string]any) int {
		r := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
		testutil.SetPathParam(r, "integration_id", f.d.ID.String())
		require.NoError(t, f.a.UpdateInboxIntegration(r))
		return testutil.GetResponseStatusCode(r)
	}
	newer := make(map[string]any, len(stale))
	for key, value := range stale {
		newer[key] = value
	}
	newer["enabled"], newer["auth_mode"], newer["credential"] = false, "api_key", "replacement-synthetic-credential"
	require.Equal(t, 200, save(newer))
	var current models.InboxIntegration
	require.NoError(t, f.a.DB.First(&current, "id=?", f.d.ID).Error)
	credential := current.Credential
	require.Equal(t, 409, save(stale))
	delete(stale, "updated_at")
	require.Equal(t, 409, save(stale), "unversioned old clients must refresh rather than overwrite security settings")
	require.NoError(t, f.a.DB.First(&current, "id=?", f.d.ID).Error)
	require.False(t, current.Enabled)
	require.Equal(t, "api_key", current.AuthMode)
	require.Equal(t, credential, current.Credential)
}
