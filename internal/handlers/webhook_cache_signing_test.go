package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func signedCacheFixture(t *testing.T) (*App, *models.Webhook) {
	t.Helper()
	app := cacheTestApp(t)
	app.Config.App.EncryptionKey = "webhook-cache-fixture-key-32-bytes"
	org := testutil.CreateTestOrganization(t, app.DB)
	hook := &models.Webhook{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		Name: "Signed cache fixture", URL: "https://receiver.example.invalid/events", IsActive: true,
		Events: models.StringArray{"message.incoming"}, Secret: "fixture-signing-secret",
		Headers: models.JSONB{"Authorization": "Bearer fixture-private-header"}}
	require.NoError(t, app.DB.Create(hook).Error)
	t.Cleanup(func() { app.InvalidateWebhooksCache(org.ID) })
	return app, hook
}

func TestWebhookSigningSurvivesWarmCacheDelivery(t *testing.T) {
	app, hook := signedCacheFixture(t)
	var mu sync.Mutex
	var valid []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		mac := hmac.New(sha256.New, []byte(hook.Secret))
		_, _ = mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		mu.Lock()
		valid = append(valid, hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Webhook-Signature"))))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()
	require.NoError(t, app.DB.Model(hook).Update("url", server.URL).Error)
	app.HTTPClient = &http.Client{Timeout: time.Second}
	for i := 0; i < 3; i++ {
		app.DispatchWebhook(hook.OrganizationID, models.WebhookEventMessageIncoming, map[string]int{"sequence": i})
		app.WaitForBackgroundTasks()
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []bool{true, true, true}, valid, "every delivery must be signed, including warm cache hits")
	cached, err := app.Redis.Get(context.Background(), fmt.Sprintf("%s%s", webhooksCachePrefix, hook.OrganizationID)).Result()
	require.NoError(t, err)
	assert.True(t, crypto.IsEncrypted(cached))
	assert.NotContains(t, cached, hook.Secret)
	assert.NotContains(t, cached, "fixture-private-header")
	api, err := json.Marshal(hook)
	require.NoError(t, err)
	assert.NotContains(t, string(api), hook.Secret, "fix must not expose the secret through model JSON")
}

func TestWebhookSigningReloadsLegacyCorruptOrWrongKeyCache(t *testing.T) {
	for _, mode := range []string{"legacy", "corrupt", "wrong-key"} {
		t.Run(mode, func(t *testing.T) {
			app, hook := signedCacheFixture(t)
			_, err := app.getWebhooksCached(hook.OrganizationID)
			require.NoError(t, err)
			key := fmt.Sprintf("%s%s", webhooksCachePrefix, hook.OrganizationID)
			switch mode {
			case "legacy":
				old := *hook
				old.URL = "https://stale.example.invalid"
				data, err := json.Marshal([]models.Webhook{old})
				require.NoError(t, err)
				require.NoError(t, app.Redis.Set(context.Background(), key, data, time.Hour).Err())
			case "corrupt":
				require.NoError(t, app.Redis.Set(context.Background(), key, "enc:broken", time.Hour).Err())
			case "wrong-key":
				app.Config.App.EncryptionKey = "different-fixture-encryption-key"
			}
			got, err := app.getWebhooksCached(hook.OrganizationID)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, hook.Secret, got[0].Secret)
			assert.Equal(t, hook.URL, got[0].URL)
		})
	}
}

func TestWebhookSigningNoKeyBypassesSensitiveCache(t *testing.T) {
	app, hook := signedCacheFixture(t)
	app.Config.App.EncryptionKey = ""
	for i := 0; i < 2; i++ {
		got, err := app.getWebhooksCached(hook.OrganizationID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, hook.Secret, got[0].Secret)
	}
	assert.Equal(t, int64(0), app.Redis.Exists(context.Background(), fmt.Sprintf("%s%s", webhooksCachePrefix, hook.OrganizationID)).Val())
}

func TestWebhookSigningMissingConfigUsesDatabase(t *testing.T) {
	app, hook := signedCacheFixture(t)
	app.Config = nil
	got, err := app.getWebhooksCached(hook.OrganizationID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, hook.Secret, got[0].Secret)
}

func TestWebhookSigningCacheCannotCrossOrganizations(t *testing.T) {
	app, hook := signedCacheFixture(t)
	_, err := app.getWebhooksCached(hook.OrganizationID)
	require.NoError(t, err)
	cached := app.Redis.Get(context.Background(), fmt.Sprintf("%s%s", webhooksCachePrefix, hook.OrganizationID)).Val()
	other := testutil.CreateTestOrganization(t, app.DB)
	key := fmt.Sprintf("%s%s", webhooksCachePrefix, other.ID)
	t.Cleanup(func() { app.InvalidateWebhooksCache(other.ID) })
	require.NoError(t, app.Redis.Set(context.Background(), key, cached, time.Hour).Err())
	got, err := app.getWebhooksCached(other.ID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestWebhookSigningCacheInvalidationRestoresCurrentConfiguration(t *testing.T) {
	app, hook := signedCacheFixture(t)
	_, err := app.getWebhooksCached(hook.OrganizationID)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(hook).Update("secret", "rotated-fixture-secret").Error)
	app.InvalidateWebhooksCache(hook.OrganizationID)
	got, err := app.getWebhooksCached(hook.OrganizationID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "rotated-fixture-secret", got[0].Secret)
	require.NoError(t, app.DB.Model(hook).Update("is_active", false).Error)
	app.InvalidateWebhooksCache(hook.OrganizationID)
	got, err = app.getWebhooksCached(hook.OrganizationID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestWebhookSigningInvalidCacheAndDatabaseFailureNeverDispatch(t *testing.T) {
	app, hook := signedCacheFixture(t)
	key := fmt.Sprintf("%s%s", webhooksCachePrefix, hook.OrganizationID)
	legacy, err := json.Marshal([]models.Webhook{*hook})
	require.NoError(t, err)
	require.NoError(t, app.Redis.Set(context.Background(), key, legacy, time.Hour).Err())
	// A failing DB lookup must not fall back to the unsigned legacy entry.
	app.DB = app.DB.Session(&gorm.Session{NewDB: true}).Table("missing_webhook_fixture_table")
	got, err := app.getWebhooksCached(hook.OrganizationID)
	require.Error(t, err)
	assert.Nil(t, got)
}
