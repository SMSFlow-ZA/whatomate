package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxLiveReloadReconcilesOnlyCurrentViewerPermissionCaches(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	f.a.Redis = testutil.SetupTestRedis(t)
	if f.a.Redis == nil {
		t.Skip("TEST_REDIS_URL required")
	}
	ctx := context.Background()
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "live-cache-role", []string{"chat:read", "chat:write", "contacts:read"})
	require.NoError(t, f.db.Model(&models.UserOrganization{}).Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Update("role_id", role.ID).Error)
	require.True(t, f.a.HasPermission(f.agent.ID, models.ResourceContacts, models.ActionRead, f.org.ID))
	keys, err := f.a.GetRolePermissionsCached(role.ID)
	require.NoError(t, err)
	require.Contains(t, keys, "contacts:read")
	other := userPermissionsCachePrefix + uuid.NewString() + ":" + uuid.NewString()
	require.NoError(t, f.a.Redis.Set(ctx, other, "synthetic untouched cache", time.Minute).Err())
	t.Cleanup(func() { require.NoError(t, f.a.Redis.Del(ctx, other).Err()) })
	require.NoError(t, f.db.Exec("DELETE FROM role_permissions rp USING permissions p WHERE rp.permission_id=p.id AND rp.custom_role_id=? AND p.resource=? AND p.action=?", role.ID, models.ResourceContacts, models.ActionRead).Error)
	require.True(t, f.a.HasPermission(f.agent.ID, models.ResourceContacts, models.ActionRead, f.org.ID), "the ordinary cache is deliberately warm before the live check")
	decision, err := f.a.inboxLiveAuthorizer(f.org.ID, f.agent.ID, nil)(ctx, inboxlive.WatchScope{Kind: "queue"}, &inboxlive.Event{OrganizationID: f.org.ID, Kind: "access", ResourceID: f.org.ID, Version: 1})
	require.NoError(t, err)
	require.True(t, decision.Allowed && decision.Match && decision.Reset)
	require.False(t, f.a.HasPermission(f.agent.ID, models.ResourceContacts, models.ActionRead, f.org.ID), "the reload cannot inherit the old cached contact privilege")
	keys, err = f.a.GetRolePermissionsCached(role.ID)
	require.NoError(t, err)
	require.NotContains(t, keys, "contacts:read")
	value, err := f.a.Redis.Get(ctx, other).Result()
	require.NoError(t, err)
	require.Equal(t, "synthetic untouched cache", value)
	// Reconnect/renewal must also reconcile a missed access notification.
	require.NoError(t, f.a.Redis.Set(ctx, userPermissionsCachePrefix+f.agent.ID.String()+":"+f.org.ID.String(), `{invalid`, time.Minute).Err())
	_, err = f.a.inboxLiveAuthorizer(f.org.ID, f.agent.ID, nil)(ctx, inboxlive.WatchScope{Kind: "queue"}, nil)
	require.NoError(t, err)
	require.False(t, f.a.HasPermission(f.agent.ID, models.ResourceContacts, models.ActionRead, f.org.ID))
}
