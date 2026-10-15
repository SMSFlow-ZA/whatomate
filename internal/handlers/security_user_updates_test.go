package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestSecurityUserUpdates_StaleSnapshotCannotRestoreSecurityState(t *testing.T) {
	for _, action := range []string{"settings", "availability", "admin-profile", "admin-password", "own-password"} {
		t.Run(action, func(t *testing.T) {
			app := cacheTestApp(t)
			org := testutil.CreateTestOrganization(t, app.DB)
			role := testutil.CreateAdminRole(t, app.DB, org.ID)
			admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
			user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithPassword("old-password-123456"))
			var committed models.User
			seen := false
			const hook = "security_concurrent_user_update"
			require.NoError(t, app.DB.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
				if tx.Statement.Table != "users" || seen {
					return
				}
				seen = true
				// Commit a legitimate admin reset/disable after the outer request
				// loaded its snapshot, immediately before its SQL write.
				concurrent := testutil.NewJSONRequest(t, map[string]any{"password": "concurrent-password-123456", "is_active": false})
				testutil.SetAuthContext(concurrent, org.ID, admin.ID)
				testutil.SetPathParam(concurrent, "id", user.ID.String())
				require.NoError(t, app.UpdateUser(concurrent))
				require.Equal(t, 200, concurrent.RequestCtx.Response.StatusCode())
				require.NoError(t, app.DB.First(&committed, user.ID).Error)
				require.NotEmpty(t, committed.AuthStamp)
			}))
			defer func() { require.NoError(t, app.DB.Callback().Update().Remove(hook)) }()

			body := map[string]any{"email_notifications": true, "is_available": false, "full_name": "Updated name"}
			switch action {
			case "admin-password":
				body = map[string]any{"password": "stale-password-123456"}
			case "own-password":
				body = map[string]any{"current_password": "old-password-123456", "new_password": "stale-password-123456"}
			}
			req := testutil.NewJSONRequest(t, body)
			testutil.SetAuthContext(req, org.ID, user.ID)
			switch action {
			case "settings":
				require.NoError(t, app.UpdateCurrentUserSettings(req))
			case "availability":
				require.NoError(t, app.UpdateAvailability(req))
			case "admin-profile", "admin-password":
				testutil.SetAuthContext(req, org.ID, admin.ID)
				testutil.SetPathParam(req, "id", user.ID.String())
				require.NoError(t, app.UpdateUser(req))
			case "own-password":
				require.NoError(t, app.ChangePassword(req))
			}
			require.True(t, seen)
			var current models.User
			require.NoError(t, app.DB.First(&current, user.ID).Error)
			require.Equal(t, committed.PasswordHash, current.PasswordHash, "stale request must not undo password reset")
			require.Equal(t, committed.AuthStamp, current.AuthStamp, "stale request must not undo session invalidation")
			require.False(t, current.IsActive, "stale request must not re-enable a disabled account")
			require.NoError(t, bcrypt.CompareHashAndPassword([]byte(current.PasswordHash), []byte("concurrent-password-123456")))
			if action == "admin-password" || action == "own-password" {
				require.Equal(t, 409, req.RequestCtx.Response.StatusCode(), "stale credential changes must be retried")
			} else {
				require.Equal(t, 200, req.RequestCtx.Response.StatusCode())
			}
		})
	}
}
