package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func subscriptionFixture(t *testing.T) (*App, *models.WhatsAppAccount) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	base, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	schema := "wa_subscription_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = base.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	gdb, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	db, err := gdb.DB()
	require.NoError(t, err)
	t.Cleanup(func() { db.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	_, err = db.Exec(`CREATE TABLE whatsapp_accounts(id uuid PRIMARY KEY,organization_id uuid,name text,phone_id text,business_id text,app_id text,access_token text,app_secret text,webhook_verify_token text,api_version text,status text,deleted_at timestamptz,created_at timestamptz,updated_at timestamptz)`)
	require.NoError(t, err)
	account := &models.WhatsAppAccount{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: uuid.New(), Name: "synthetic channel", PhoneID: "phone-a", BusinessID: "waba-a", APIVersion: "v25.0", AccessToken: "synthetic-access-token", WebhookVerifyToken: "synthetic-verification-token", Status: "active"}
	_, err = db.Exec(`INSERT INTO whatsapp_accounts(id,organization_id,name,phone_id,business_id,access_token,webhook_verify_token,api_version,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, account.ID, account.OrganizationID, account.Name, account.PhoneID, account.BusinessID, account.AccessToken, account.WebhookVerifyToken, account.APIVersion, account.Status)
	require.NoError(t, err)
	return &App{DB: gdb, Config: &config.Config{}, Log: testutil.NopLogger()}, account
}

func subscriptionBinding(account *models.WhatsAppAccount) config.WhatsAppWebhookSubscriptionOverride {
	return config.WhatsAppWebhookSubscriptionOverride{OrganizationID: account.OrganizationID.String(), AccountID: account.ID.String(), BusinessID: account.BusinessID, CallbackURL: "https://hooks.example.test/api/webhook"}
}

func TestWhatsAppSubscriptionActualRouteAndChallenge(t *testing.T) {
	a, account := subscriptionFixture(t)
	calls := 0
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v25.0/waba-a/subscribed_apps", r.URL.Path)
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	a.WhatsApp = whatsapp.NewWithBaseURL(a.Log, srv.URL)
	for _, configured := range []bool{false, true} {
		if configured {
			a.Config.WhatsApp.WebhookSubscriptionOverrides = []config.WhatsAppWebhookSubscriptionOverride{subscriptionBinding(account)}
		}
		r := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(r, account.OrganizationID, uuid.New())
		testutil.SetPathParam(r, "id", account.ID.String())
		require.NoError(t, a.SubscribeApp(r))
		require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
		if !configured {
			require.Empty(t, body)
		} else {
			var value map[string]string
			require.NoError(t, json.Unmarshal(body, &value))
			require.Equal(t, map[string]string{"override_callback_uri": "https://hooks.example.test/api/webhook", "verify_token": account.WebhookVerifyToken}, value)
		}
	}
	require.Equal(t, 2, calls)
	for _, token := range []string{account.WebhookVerifyToken, "wrong-synthetic-token", ""} {
		r := testutil.NewGETRequest(t)
		testutil.SetQueryParam(r, "hub.mode", "subscribe")
		testutil.SetQueryParam(r, "hub.verify_token", token)
		testutil.SetQueryParam(r, "hub.challenge", "12345")
		require.NoError(t, a.WebhookVerify(r))
		if token == account.WebhookVerifyToken {
			require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
			require.Equal(t, "12345", string(r.RequestCtx.Response.Body()))
		} else {
			require.Equal(t, 403, r.RequestCtx.Response.StatusCode())
			require.NotContains(t, string(r.RequestCtx.Response.Body()), token+"12345")
		}
	}
	// A changed current WABA cannot silently resubscribe to the shared default.
	require.NoError(t, a.DB.Model(&models.WhatsAppAccount{}).Where("id = ?", account.ID).Update("business_id", "waba-changed").Error)
	r := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(r, account.OrganizationID, uuid.New())
	testutil.SetPathParam(r, "id", account.ID.String())
	require.NoError(t, a.SubscribeApp(r))
	require.Equal(t, 400, r.RequestCtx.Response.StatusCode())
	require.Equal(t, 2, calls)
}

func TestWhatsAppSubscriptionMappingFailsClosed(t *testing.T) {
	account := &models.WhatsAppAccount{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: uuid.New(), BusinessID: "waba-a", WebhookVerifyToken: "synthetic-token"}
	b := subscriptionBinding(account)
	for _, mutate := range []func(*config.WhatsAppWebhookSubscriptionOverride){
		func(b *config.WhatsAppWebhookSubscriptionOverride) { b.OrganizationID = uuid.NewString() },
		func(b *config.WhatsAppWebhookSubscriptionOverride) { b.AccountID = uuid.NewString() },
		func(b *config.WhatsAppWebhookSubscriptionOverride) { b.BusinessID = "waba-changed" },
		func(b *config.WhatsAppWebhookSubscriptionOverride) {
			b.CallbackURL = "http://hooks.example.test/webhook"
		},
	} {
		changed := b
		mutate(&changed)
		a := &App{Config: &config.Config{}}
		a.Config.WhatsApp.WebhookSubscriptionOverrides = []config.WhatsAppWebhookSubscriptionOverride{changed}
		_, err := a.whatsAppSubscriptionAccount(account)
		require.Error(t, err)
	}
	a := &App{Config: &config.Config{}}
	a.Config.WhatsApp.WebhookSubscriptionOverrides = []config.WhatsAppWebhookSubscriptionOverride{b, b}
	_, err := a.whatsAppSubscriptionAccount(account)
	require.Error(t, err)
	a.Config.WhatsApp.WebhookSubscriptionOverrides = []config.WhatsAppWebhookSubscriptionOverride{b}
	unrelated := *account
	unrelated.ID, unrelated.BusinessID = uuid.New(), "another-waba"
	legacy, err := a.whatsAppSubscriptionAccount(&unrelated)
	require.NoError(t, err)
	require.Nil(t, legacy.SubscriptionOverride)
	account.WebhookVerifyToken = ""
	_, err = a.whatsAppSubscriptionAccount(account)
	require.Error(t, err)
	account.WebhookVerifyToken = "synthetic-token"
	account.ID = uuid.Nil
	_, err = a.whatsAppSubscriptionAccount(account)
	require.Error(t, err)
}

func TestWhatsAppSubscriptionVerificationDoesNotLogSQLToken(t *testing.T) {
	a, account := subscriptionFixture(t)
	var output bytes.Buffer
	a.DB = a.DB.Session(&gorm.Session{Logger: logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info})})
	for _, token := range []string{account.WebhookVerifyToken, "unknown-private-verification-token"} {
		r := testutil.NewGETRequest(t)
		testutil.SetQueryParam(r, "hub.mode", "subscribe")
		testutil.SetQueryParam(r, "hub.verify_token", token)
		testutil.SetQueryParam(r, "hub.challenge", "12345")
		require.NoError(t, a.WebhookVerify(r))
		require.NotContains(t, output.String(), token)
	}
}

func TestWhatsAppSubscriptionEmbeddedSignupPreservesVerifiedRouting(t *testing.T) {
	for _, mode := range []string{"mismatched-binding", "provider-failure", "success"} {
		t.Run(mode, func(t *testing.T) {
			a, account := subscriptionFixture(t)
			redisURL := os.Getenv("TEST_REDIS_URL")
			if redisURL == "" {
				t.Skip("TEST_REDIS_URL required")
			}
			opts, err := redis.ParseURL(redisURL)
			require.NoError(t, err)
			a.Redis = redis.NewClient(opts)
			require.NoError(t, a.Redis.Ping(context.Background()).Err())
			defer a.Redis.Close()
			account.PhoneID = "synthetic-" + uuid.NewString()
			require.NoError(t, a.DB.Model(&models.WhatsAppAccount{}).Where("id = ?", account.ID).Update("phone_id", account.PhoneID).Error)
			db, err := a.DB.DB()
			require.NoError(t, err)
			_, err = db.Exec(`ALTER TABLE whatsapp_accounts ADD COLUMN is_default_incoming boolean, ADD COLUMN is_default_outgoing boolean, ADD COLUMN auto_read_receipt boolean, ADD COLUMN business_calling_enabled boolean, ADD COLUMN is_smb boolean, ADD COLUMN pin text, ADD COLUMN created_by_id uuid, ADD COLUMN updated_by_id uuid;
CREATE TABLE organizations(id uuid PRIMARY KEY,settings jsonb,deleted_at timestamptz);
CREATE TABLE users(id uuid PRIMARY KEY,organization_id uuid,role_id uuid,is_active boolean,is_super_admin boolean,name text,deleted_at timestamptz)`)
			require.NoError(t, err)
			require.NoError(t, a.DB.AutoMigrate(&models.AuditLog{}))
			user := uuid.New()
			_, err = db.Exec(`INSERT INTO organizations VALUES($1,'{}',NULL)`, account.OrganizationID)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO users VALUES($1,$2,NULL,true,true,'Synthetic operator',NULL)`, user, account.OrganizationID)
			require.NoError(t, err)
			a.Config.App.EncryptionKey = "synthetic-test-encryption-only"
			a.Config.WhatsApp.AppID = "synthetic-app"
			a.Config.WhatsApp.AppSecret = "synthetic-app-secret"
			a.Config.WhatsApp.APIVersion = "v25.0"
			binding := subscriptionBinding(account)
			if mode == "mismatched-binding" {
				binding.AccountID = uuid.NewString()
			}
			a.Config.WhatsApp.WebhookSubscriptionOverrides = []config.WhatsAppWebhookSubscriptionOverride{binding}
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "oauth/access_token"):
					_, _ = w.Write([]byte(`{"access_token":"synthetic-new-access-token"}`))
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, account.PhoneID):
					_, _ = w.Write([]byte(`{"id":"synthetic-phone","is_on_biz_app":true,"platform_type":"SMB"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/v25.0/waba-a/subscribed_apps":
					posts++
					var payload map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
					require.Equal(t, account.WebhookVerifyToken, payload["verify_token"])
					require.Equal(t, binding.CallbackURL, payload["override_callback_uri"])
					if mode == "provider-failure" {
						w.WriteHeader(400)
						_, _ = w.Write([]byte(`{"error":{"code":100,"message":"synthetic-verification-token"}}`))
					} else {
						_, _ = w.Write([]byte(`{"success":true}`))
					}
				default:
					t.Errorf("unexpected provider operation %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			a.WhatsApp = whatsapp.NewWithBaseURL(a.Log, srv.URL)
			r := testutil.NewJSONRequest(t, map[string]string{"code": "synthetic-code", "phone_id": account.PhoneID, "waba_id": account.BusinessID, "name": account.Name, "webhook_verify_token": "browser-replacement-must-not-be-used"})
			testutil.SetAuthContext(r, account.OrganizationID, user)
			require.NoError(t, a.ExchangeToken(r))
			if mode == "mismatched-binding" {
				require.Equal(t, 400, r.RequestCtx.Response.StatusCode())
				require.Zero(t, posts)
				return
			}
			require.Equal(t, 1, posts)
			if mode == "provider-failure" {
				require.Equal(t, 502, r.RequestCtx.Response.StatusCode())
				require.NotContains(t, string(r.RequestCtx.Response.Body()), account.WebhookVerifyToken)
			} else {
				require.Equal(t, 200, r.RequestCtx.Response.StatusCode())
			}
			var saved models.WhatsAppAccount
			require.NoError(t, a.DB.First(&saved, "id = ?", account.ID).Error)
			require.Equal(t, account.WebhookVerifyToken, saved.WebhookVerifyToken)
			require.True(t, strings.HasPrefix(saved.AccessToken, "enc:"))
			a.decryptAccountSecrets(&saved)
			require.Equal(t, "synthetic-new-access-token", saved.AccessToken)
			// Audit writes are asynchronous; finish them before dropping this schema.
			require.Eventually(t, func() bool { var count int64; a.DB.Model(&models.AuditLog{}).Count(&count); return count == 1 }, time.Second, 10*time.Millisecond)
		})
	}
}
