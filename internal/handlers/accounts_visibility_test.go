package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestApp_AccountVerificationVisibility(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions []string
		superadmin  bool
		mayManage   bool
	}{
		{"reader", []string{"accounts:read"}, false, false},
		{"account_manager", []string{"accounts:read", "accounts:write"}, false, true},
		{"superadmin", nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t)
			org := testutil.CreateTestOrganization(t, app.DB)
			role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "account-visibility", tc.permissions)
			opts := []testutil.UserOption{testutil.WithRoleID(&role.ID)}
			if tc.superadmin {
				opts = append(opts, testutil.WithSuperAdmin())
			}
			user := testutil.CreateTestUser(t, app.DB, org.ID, opts...)
			account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
			require.NotEmpty(t, account.WebhookVerifyToken)

			for _, endpoint := range []string{"list", "detail"} {
				t.Run(endpoint, func(t *testing.T) {
					req := testutil.NewGETRequest(t)
					testutil.SetAuthContext(req, org.ID, user.ID)
					if endpoint == "detail" {
						testutil.SetPathParam(req, "id", account.ID.String())
						require.NoError(t, app.GetAccount(req))
					} else {
						require.NoError(t, app.ListAccounts(req))
					}
					require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
					var body map[string]any
					require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &body))
					data := body["data"].(map[string]any)
					if endpoint == "list" {
						data = data["accounts"].([]any)[0].(map[string]any)
					}
					require.Equal(t, account.ID.String(), data["id"])
					if tc.mayManage {
						require.Equal(t, account.WebhookVerifyToken, data["webhook_verify_token"])
					} else {
						require.NotContains(t, data, "webhook_verify_token", "read-only account access must not expose webhook verification material")
					}
					require.NotContains(t, data, "access_token")
					require.NotContains(t, data, "app_secret")
				})
			}
		})
	}
}
