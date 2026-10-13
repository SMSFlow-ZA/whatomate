package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestPlanStatusRequiresAuthenticationAndNeverExposesBootstrapSecret(t *testing.T) {
	app := &handlers.App{Config: &config.Config{Plan: config.PlanConfig{SupportPassword: "private-bootstrap-value"}}}
	anonymous := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPlan(anonymous))
	require.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(anonymous))
	require.NotContains(t, string(testutil.GetResponseBody(anonymous)), "private-bootstrap-value")

	authenticated := testutil.NewGETRequest(t)
	testutil.SetAuthContext(authenticated, uuid.New(), uuid.New())
	require.NoError(t, app.GetPlan(authenticated))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(authenticated))
	var response struct {
		Data struct {
			Enabled  bool `json:"enabled"`
			Enforced bool `json:"enforced"`
		}
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(authenticated), &response))
	require.False(t, response.Data.Enabled)
	require.False(t, response.Data.Enforced)
	require.NotContains(t, string(testutil.GetResponseBody(authenticated)), "private-bootstrap-value")
}

func TestCorrespondingSourceIsPublicAndUsesTheDeployedCommit(t *testing.T) {
	source := "https://github.com/example/whatomate/commit/" + strings.Repeat("a", 40)
	app := &handlers.App{Config: &config.Config{Plan: config.PlanConfig{Enabled: true, SourceURL: source, SupportPassword: "private-bootstrap-value"}}}
	request := testutil.NewGETRequest(t)
	require.NoError(t, app.CorrespondingSource(request))
	require.Equal(t, fasthttp.StatusFound, testutil.GetResponseStatusCode(request))
	require.Equal(t, source, string(request.RequestCtx.Response.Header.Peek("Location")))
	require.NotContains(t, string(testutil.GetResponseBody(request)), "private-bootstrap-value")
}
