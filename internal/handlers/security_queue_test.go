package handlers

import (
	"encoding/json"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSecurityQueuePick_FilterAndCurrentMembership(t *testing.T) {
	for _, scenario := range []string{"invalid", "invalid-writer", "own-team", "other-team", "default-scope", "disabled", "membership-race"} {
		t.Run(scenario, func(t *testing.T) {
			app := cacheTestApp(t)
			org := testutil.CreateTestOrganization(t, app.DB)
			keys := []string{"transfers:pickup"}
			if scenario == "invalid-writer" {
				keys = []string{"transfers:write", "transfers:pickup"}
			}
			role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "queue-filter", keys)
			user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
			contact := testutil.CreateTestContact(t, app.DB, org.ID)
			team := models.Team{OrganizationID: org.ID, Name: "restricted-queue"}
			require.NoError(t, app.DB.Create(&team).Error)
			transfer := models.AgentTransfer{OrganizationID: org.ID, ContactID: contact.ID, TeamID: &team.ID, Status: models.TransferStatusActive}
			require.NoError(t, app.DB.Create(&transfer).Error)
			if scenario == "own-team" || scenario == "membership-race" || scenario == "disabled" {
				require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: user.ID}).Error)
			}
			if scenario == "disabled" {
				settings := models.ChatbotSettings{OrganizationID: org.ID}
				require.NoError(t, app.DB.Create(&settings).Error)
				require.NoError(t, app.DB.Model(&settings).Update("allow_agent_queue_pickup", false).Error)
			}
			if scenario == "membership-race" {
				seen := false
				const hook = "security_remove_membership_before_queue_claim"
				require.NoError(t, app.DB.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
					if tx.Statement.Table != "agent_transfers" || seen {
						return
					}
					seen = true
					require.NoError(t, app.DB.Where("team_id = ? AND user_id = ?", team.ID, user.ID).Delete(&models.TeamMember{}).Error)
				}))
				defer func() { require.NoError(t, app.DB.Callback().Update().Remove(hook)) }()
			}
			req := testutil.NewJSONRequest(t, map[string]any{})
			req.RequestCtx.Request.SetRequestURI("/api/chatbot/transfers/pick")
			testutil.SetAuthContext(req, org.ID, user.ID)
			filter := team.ID.String()
			switch scenario {
			case "invalid", "invalid-writer":
				filter = "not-a-uuid"
			case "default-scope":
				filter = ""
			}
			testutil.SetQueryParam(req, "team_id", filter)
			require.NotNil(t, app.AuthorizeAPI()(req))
			require.NoError(t, app.PickNextTransfer(req))
			var current models.AgentTransfer
			require.NoError(t, app.DB.First(&current, transfer.ID).Error)
			switch scenario {
			case "own-team":
				require.Equal(t, 200, req.RequestCtx.Response.StatusCode())
				require.Equal(t, &user.ID, current.AgentID)
			case "default-scope":
				require.Equal(t, 200, req.RequestCtx.Response.StatusCode())
				var response struct {
					Data map[string]any `json:"data"`
				}
				require.NoError(t, json.Unmarshal(req.RequestCtx.Response.Body(), &response))
				require.Nil(t, response.Data["transfer"])
				require.Nil(t, current.AgentID)
			default:
				expected := 403
				switch scenario {
				case "invalid", "invalid-writer":
					expected = 400
				case "membership-race":
					expected = 409
				}
				require.Equal(t, expected, req.RequestCtx.Response.StatusCode())
				require.Nil(t, current.AgentID)
			}
		})
	}
}
