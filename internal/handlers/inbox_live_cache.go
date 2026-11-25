package handlers

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/redis/go-redis/v9"
)

// A live reset reloads through ordinary HTTP APIs, some of which retain the
// existing permission cache. Compare only this authenticated viewer's cache to
// fresh database authority before signalling the reload. Never scan or clear
// another viewer's keys or another organisation-specific user cache. A role
// cache belongs to the freshly verified role in this organisation. No cache
// serves as the authority.
func (a *App) reconcileInboxLivePermissionCache(ctx context.Context, viewer auditViewer) error {
	if a.Redis == nil {
		return nil
	}
	userKey := userPermissionsCachePrefix + viewer.User.String()
	orgKey := userKey + ":" + viewer.Org.String()
	cached, err := a.Redis.Get(ctx, orgKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return errAuditStorage
	}
	if err == nil {
		var permissions UserPermissions
		valid := len(cached) <= 65536 && json.Unmarshal([]byte(cached), &permissions) == nil &&
			permissions.IsSuperAdmin == viewer.Super && (viewer.Super ||
			(viewer.Role != nil && permissions.RoleID == *viewer.Role && inboxLivePermissionKeysEqual(permissions.Permissions, viewer)))
		if !valid {
			if err := a.Redis.Del(ctx, orgKey, userKey).Err(); err != nil {
				return errAuditStorage
			}
		}
	}
	if viewer.Role != nil && !viewer.Super {
		key := rolePermissionsCachePrefix + viewer.Role.String()
		value, err := a.Redis.Get(ctx, key).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return errAuditStorage
		}
		if err == nil {
			var keys []string
			if len(value) > 65536 || json.Unmarshal([]byte(value), &keys) != nil || !inboxLivePermissionKeysEqual(keys, viewer) {
				if err := a.Redis.Del(ctx, key).Err(); err != nil {
					return errAuditStorage
				}
			}
		}
	}
	return nil
}

func inboxLivePermissionKeysEqual(keys []string, viewer auditViewer) bool {
	if len(keys) != len(viewer.Permissions) {
		return false
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !viewer.Permissions[key] || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
