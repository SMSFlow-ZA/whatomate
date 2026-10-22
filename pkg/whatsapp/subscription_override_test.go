package whatsapp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestSubscribeAppExplicitOverrideAndLegacyBody(t *testing.T) {
	for _, override := range []*whatsapp.WebhookSubscriptionOverride{nil, {CallbackURL: "https://hooks.example.test/api/webhook", VerifyToken: "synthetic-verification-token"}} {
		t.Run(map[bool]string{true: "override", false: "legacy"}[override != nil], func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/v25.0/waba-a/subscribed_apps", r.URL.Path)
				require.Equal(t, "Bearer synthetic-access-token", r.Header.Get("Authorization"))
				require.Empty(t, r.URL.RawQuery)
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				if override == nil {
					require.Empty(t, body)
				} else {
					var value map[string]string
					require.NoError(t, json.Unmarshal(body, &value))
					require.Equal(t, map[string]string{"override_callback_uri": override.CallbackURL, "verify_token": override.VerifyToken}, value)
				}
				_, _ = w.Write([]byte(`{"success":true}`))
			}))
			defer srv.Close()
			account := &whatsapp.Account{BusinessID: "waba-a", APIVersion: "v25.0", AccessToken: "synthetic-access-token", SubscriptionOverride: override}
			require.NoError(t, whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL).SubscribeApp(context.Background(), account))
			require.Equal(t, 1, calls)
		})
	}
}

func TestSubscribeAppInvalidOverrideNeverCallsProvider(t *testing.T) {
	for _, value := range []whatsapp.WebhookSubscriptionOverride{
		{}, {CallbackURL: "https://hooks.example.test/webhook"}, {VerifyToken: "synthetic-token"},
		{CallbackURL: "http://hooks.example.test/webhook", VerifyToken: "synthetic-token"},
		{CallbackURL: "https://user:password@hooks.example.test/webhook", VerifyToken: "synthetic-token"},
		{CallbackURL: "https://hooks.example.test/webhook#fragment", VerifyToken: "synthetic-token"},
		{CallbackURL: "https://hooks.example.test/" + strings.Repeat("x", 200), VerifyToken: "synthetic-token"},
	} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{"success":true}`)) }))
		account := &whatsapp.Account{BusinessID: "waba-a", APIVersion: "v25.0", AccessToken: "synthetic-access-token", SubscriptionOverride: &value}
		err := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL).SubscribeApp(context.Background(), account)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-token")
		require.NotContains(t, err.Error(), value.CallbackURL+"synthetic-token")
		require.Zero(t, calls)
		srv.Close()
	}
}

func TestSubscribeAppOverrideFailureHasNoLegacyFallback(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Before override the current callback uri, subscribe the app; synthetic-token","code":100}}`))
	}))
	defer srv.Close()
	account := &whatsapp.Account{BusinessID: "waba-a", APIVersion: "v25.0", AccessToken: "synthetic-access-token", SubscriptionOverride: &whatsapp.WebhookSubscriptionOverride{CallbackURL: "https://hooks.example.test/api/webhook", VerifyToken: "synthetic-token"}}
	err := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL).SubscribeApp(context.Background(), account)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-token")
	require.Equal(t, 1, calls)
}

func TestSubscribeAppOverrideNeverForwardsVerificationOnRedirect(t *testing.T) {
	forwarded := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	account := &whatsapp.Account{BusinessID: "waba-a", APIVersion: "v25.0", AccessToken: "synthetic-access-token", SubscriptionOverride: &whatsapp.WebhookSubscriptionOverride{CallbackURL: "https://hooks.example.test/api/webhook", VerifyToken: "synthetic-token"}}
	err := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL).SubscribeApp(context.Background(), account)
	require.Error(t, err)
	require.Zero(t, forwarded)
}
