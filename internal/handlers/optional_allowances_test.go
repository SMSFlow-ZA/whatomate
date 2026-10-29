package handlers

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOptionalAllowancesReturnStructuredLimitsAndReleaseFailedTransactions(t *testing.T) {
	app, org, owner, _ := managedEntityApp(t)
	r := testutil.NewJSONRequest(t, CreateOrganizationRequest{Name: "Additional"})
	testutil.SetAuthContext(r, org.ID, owner.ID)
	require.NoError(t, app.CreateOrganization(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	require.Contains(t, string(testutil.GetResponseBody(r)), "organizations")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, plan.Install(app.DB.WithContext(ctx), app.Config.Plan), "quota failure must release its transaction and table locks")
	r = testutil.NewJSONRequest(t, UserRequest{Email: "additional@example.invalid", Password: "password123", FullName: "Additional"})
	testutil.SetAuthContext(r, org.ID, owner.ID)
	require.NoError(t, app.CreateUser(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	require.Contains(t, string(testutil.GetResponseBody(r)), "users")
	require.NoError(t, app.DB.Exec("INSERT INTO users(id,email,organization_id,deleted_at) VALUES (?,?,?,now())", uuid.New(), "restore@example.invalid", org.ID).Error)
	r = testutil.NewJSONRequest(t, UserRequest{Email: "restore@example.invalid", Password: "password123", FullName: "Restored"})
	testutil.SetAuthContext(r, org.ID, owner.ID)
	require.NoError(t, app.CreateUser(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, app.DB.Model(&models.User{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
func TestOptionalAllowancesUsageDeniesOrdinaryUser(t *testing.T) {
	app, org, _, _ := managedEntityApp(t)
	cfg := app.Config.Plan
	cfg.MaxUsers = 2
	require.NoError(t, plan.Install(app.DB, cfg))
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, app.GetPlan(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
}

func TestAllowancesStatusReadsInstalledEntityLimits(t *testing.T) {
	app, org, owner, _ := managedEntityApp(t)
	current := app.Config.Plan
	current.MaxUsers = 2
	current.MaxNumbers = 3
	current.MaxOrganizations = 0
	require.NoError(t, plan.Install(app.DB, current))
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, owner.ID)
	require.NoError(t, app.GetPlan(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var response struct {
		Data struct {
			Limits struct {
				Users         int `json:"max_users"`
				Numbers       int `json:"max_numbers"`
				Organizations int `json:"max_organizations"`
			}
			Usage struct {
				Users         int `json:"users"`
				Organizations int `json:"organizations"`
			}
		}
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(r), &response))
	require.Equal(t, 2, response.Data.Limits.Users)
	require.Equal(t, 3, response.Data.Limits.Numbers)
	require.Zero(t, response.Data.Limits.Organizations)
	require.Equal(t, 1, response.Data.Usage.Users)
	require.Equal(t, 1, response.Data.Usage.Organizations)
	require.NotContains(t, string(testutil.GetResponseBody(r)), "storage")
}
