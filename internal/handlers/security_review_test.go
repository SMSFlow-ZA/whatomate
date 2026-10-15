package handlers

import (
	"context"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReviewSecurityPasswordResetPreSaveLoginRevoked(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithPassword("old-password-123456"))
	var mintedAccess, mintedRefresh string
	seen := false
	callbackName := "review_login_after_epoch_before_save"
	require.NoError(t, app.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "users" || seen {
			return
		}
		seen = true
		epoch, err := middleware.SessionVersion(context.Background(), app.Redis, user.ID)
		require.NoError(t, err)
		require.NotEmpty(t, epoch, "password change has already revoked the prior epoch")
		login := testutil.NewJSONRequest(t, map[string]string{"email": user.Email, "password": "old-password-123456"})
		require.NoError(t, app.Login(login))
		require.Equal(t, 200, login.RequestCtx.Response.StatusCode(), string(login.RequestCtx.Response.Body()))
		mintedAccess = testutil.GetResponseCookie(login, "whm_access")
		mintedRefresh = testutil.GetResponseCookie(login, "whm_refresh")
	}))
	defer app.DB.Callback().Update().Remove(callbackName)
	reset := testutil.NewJSONRequest(t, map[string]string{"current_password": "old-password-123456", "new_password": "new-password-123456"})
	testutil.SetAuthContext(reset, org.ID, user.ID)
	require.NoError(t, app.ChangePassword(reset))
	require.Equal(t, 200, reset.RequestCtx.Response.StatusCode(), string(reset.RequestCtx.Response.Body()))
	require.True(t, seen)
	require.NotEmpty(t, mintedAccess)
	require.NotEmpty(t, mintedRefresh)
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, mintedAccess, "access")
	require.NoError(t, err)
	require.Error(t, middleware.ValidateSession(context.Background(), app.Redis, claims), "login with the old password during the reset must not survive the completed reset")
}

func TestReviewSecurityLogoutDuringRefreshRevokesSuccessor(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	refresh, err := app.generateRefreshToken(user, access)
	require.NoError(t, err)
	loggedOut := false
	callbackName := "review_logout_during_refresh"
	require.NoError(t, app.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "users" || loggedOut {
			return
		}
		loggedOut = true
		logout := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
		require.NoError(t, app.Logout(logout))
		require.Equal(t, 200, logout.RequestCtx.Response.StatusCode())
	}))
	defer app.DB.Callback().Query().Remove(callbackName)
	rotate := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.RefreshToken(rotate))
	require.True(t, loggedOut)
	require.Equal(t, 401, rotate.RequestCtx.Response.StatusCode(), "a completed logout must fence an already in-flight refresh of that session")
}

func TestReviewSecurityPickupCannotStealOtherAgentsTransfer(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "pickup-only-review", []string{"transfers:pickup", "chat:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	team := models.Team{OrganizationID: org.ID, Name: "unrelated-team"}
	require.NoError(t, app.DB.Create(&team).Error)
	transfer := models.AgentTransfer{OrganizationID: org.ID, ContactID: contact.ID, AgentID: &other.ID, TeamID: &team.ID, Status: models.TransferStatusActive}
	require.NoError(t, app.DB.Create(&transfer).Error)
	req := testutil.NewJSONRequest(t, map[string]any{})
	req.RequestCtx.Request.SetRequestURI("/api/chatbot/transfers/" + transfer.ID.String() + "/assign")
	req.RequestCtx.Request.Header.SetMethod("PUT")
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", transfer.ID.String())
	require.NotNil(t, app.AuthorizeAPI()(req), "real API family policy grants pickup permission")
	require.NoError(t, app.AssignAgentTransfer(req))
	require.Equal(t, 403, req.RequestCtx.Response.StatusCode(), "pickup-only user must not steal an assigned transfer outside their teams")
}

func TestReviewSecurityPickupEligibilityAndConditionalClaim(t *testing.T) {
	for _, scenario := range []string{"general", "own-team", "other-team", "disabled", "assignment-race"} {
		t.Run(scenario, func(t *testing.T) {
			app := cacheTestApp(t)
			org := testutil.CreateTestOrganization(t, app.DB)
			role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "pickup", []string{"transfers:pickup"})
			user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
			other := testutil.CreateTestUser(t, app.DB, org.ID)
			contact := testutil.CreateTestContact(t, app.DB, org.ID)
			transfer := models.AgentTransfer{OrganizationID: org.ID, ContactID: contact.ID, Status: models.TransferStatusActive}
			if scenario == "own-team" || scenario == "other-team" {
				team := models.Team{OrganizationID: org.ID, Name: "pickup-team"}
				require.NoError(t, app.DB.Create(&team).Error)
				transfer.TeamID = &team.ID
				if scenario == "own-team" {
					require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: user.ID}).Error)
				}
			}
			if scenario == "disabled" {
				settings := models.ChatbotSettings{OrganizationID: org.ID}
				settings.AgentAssignment.AllowQueuePickup = false
				require.NoError(t, app.DB.Create(&settings).Error)
				require.NoError(t, app.DB.Model(&settings).Update("allow_agent_queue_pickup", false).Error)
			}
			require.NoError(t, app.DB.Create(&transfer).Error)
			if scenario == "assignment-race" {
				seen := false
				const hook = "review_concurrent_transfer_assignment"
				require.NoError(t, app.DB.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
					if tx.Statement.Table != "agent_transfers" || seen {
						return
					}
					seen = true
					require.NoError(t, app.DB.Model(&models.AgentTransfer{}).Where("id = ?", transfer.ID).Update("agent_id", other.ID).Error)
				}))
				defer app.DB.Callback().Update().Remove(hook)
			}
			req := testutil.NewJSONRequest(t, map[string]any{})
			testutil.SetAuthContext(req, org.ID, user.ID)
			testutil.SetPathParam(req, "id", transfer.ID.String())
			require.NoError(t, app.AssignAgentTransfer(req))
			var current models.AgentTransfer
			require.NoError(t, app.DB.First(&current, transfer.ID).Error)
			switch scenario {
			case "general", "own-team":
				require.Equal(t, 200, req.RequestCtx.Response.StatusCode())
				require.Equal(t, &user.ID, current.AgentID)
			case "assignment-race":
				require.Equal(t, 409, req.RequestCtx.Response.StatusCode())
				require.Equal(t, &other.ID, current.AgentID)
			default:
				require.Equal(t, 403, req.RequestCtx.Response.StatusCode())
				require.Nil(t, current.AgentID)
			}
		})
	}
}

func TestReviewSecurityDatabaseStampFencesFailedPostSaveRevocation(t *testing.T) {
	app := cacheTestApp(t)
	healthy := app.Redis
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithPassword("old-password-123456"))
	var access, refresh string
	before, after := "review_presave_login_storage_failure", "review_postsave_storage_failure"
	seen := false
	require.NoError(t, app.DB.Callback().Update().Before("gorm:update").Register(before, func(tx *gorm.DB) {
		if tx.Statement.Table != "users" || seen {
			return
		}
		seen = true
		login := testutil.NewJSONRequest(t, map[string]string{"email": user.Email, "password": "old-password-123456"})
		require.NoError(t, app.Login(login))
		require.Equal(t, 200, login.RequestCtx.Response.StatusCode())
		access = testutil.GetResponseCookie(login, "whm_access")
		refresh = testutil.GetResponseCookie(login, "whm_refresh")
	}))
	defer app.DB.Callback().Update().Remove(before)
	unavailable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 20 * time.Millisecond, ContextTimeoutEnabled: true})
	defer unavailable.Close()
	require.NoError(t, app.DB.Callback().Update().After("gorm:update").Register(after, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			app.Redis = unavailable
		}
	}))
	defer app.DB.Callback().Update().Remove(after)
	reset := testutil.NewJSONRequest(t, map[string]string{"current_password": "old-password-123456", "new_password": "new-password-123456"})
	testutil.SetAuthContext(reset, org.ID, user.ID)
	require.NoError(t, app.ChangePassword(reset))
	require.Equal(t, 503, reset.RequestCtx.Response.StatusCode())
	app.Redis = healthy
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, access, "access")
	require.NoError(t, err)
	// The unavailable post-save Redis write did not rotate the epoch. The DB stamp
	// must independently invalidate these raced tokens at every identity boundary.
	require.NoError(t, middleware.ValidateSession(context.Background(), healthy, claims))
	req := testutil.NewGETRequest(t)
	req.RequestCtx.Request.Header.Set("Authorization", "Bearer "+access)
	require.Nil(t, middleware.AuthWithDB(app.Config.JWT.Secret, app.DB, healthy)(req))
	require.Equal(t, 401, req.RequestCtx.Response.StatusCode())
	rotate := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.RefreshToken(rotate))
	require.Equal(t, 401, rotate.RequestCtx.Response.StatusCode())
}

func TestReviewSecurityLogoutOldRefreshRevokesRotatedFamilyOnly(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	refresh, err := app.generateRefreshToken(user, access)
	require.NoError(t, err)
	independent, err := app.generateAccessToken(user)
	require.NoError(t, err)
	rotate := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.RefreshToken(rotate))
	require.Equal(t, 200, rotate.RequestCtx.Response.StatusCode())
	successor := testutil.GetResponseCookie(rotate, "whm_access")
	logout := testutil.NewJSONRequest(t, map[string]string{"refresh_token": refresh})
	require.NoError(t, app.Logout(logout))
	require.Equal(t, 200, logout.RequestCtx.Response.StatusCode())
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, successor, "access")
	require.NoError(t, err)
	require.Error(t, middleware.ValidateSession(context.Background(), app.Redis, claims))
	claims, err = middleware.ParseToken(app.Config.JWT.Secret, independent, "access")
	require.NoError(t, err)
	require.NoError(t, middleware.ValidateSession(context.Background(), app.Redis, claims))
}

func TestReviewSecurityAuthenticatedMintCannotAdoptChangedStamp(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	access, err := app.generateAccessToken(user)
	require.NoError(t, err)
	claims, err := middleware.ParseToken(app.Config.JWT.Secret, access, "access")
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(user).Update("auth_stamp", "changed-stamp").Error)
	for _, action := range []string{"switch", "ws"} {
		t.Run(action, func(t *testing.T) {
			req := testutil.NewJSONRequest(t, map[string]string{"organization_id": org.ID.String()})
			testutil.SetAuthContext(req, org.ID, user.ID)
			req.RequestCtx.SetUserValue(middleware.ContextKeyClaims, claims)
			if action == "switch" {
				require.NoError(t, app.SwitchOrg(req))
			} else {
				require.NoError(t, app.GetWSToken(req))
			}
			require.Equal(t, 401, req.RequestCtx.Response.StatusCode(), action+" must not adopt a post-reset stamp")
		})
	}
}
