package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This provider and HTTP host exist only in the test binary. The independent
// provider ledger is test-only storage in an explicitly owned ephemeral DB;
// it is not an application model/migration or a runtime provider option.
type syntheticInboxProvider struct{ db *gorm.DB }

func newSyntheticInboxProvider(db *gorm.DB) (*syntheticInboxProvider, error) {
	private := db.Session(&gorm.Session{Logger: logger.Discard})
	if err := private.Exec("CREATE SCHEMA IF NOT EXISTS whatomate_synthetic_provider").Error; err != nil {
		return nil, err
	}
	if err := private.Exec(`CREATE TABLE IF NOT EXISTS whatomate_synthetic_provider.operations (
		operation_key text PRIMARY KEY, endpoint_key text NOT NULL,
		payload_digest text NOT NULL, result jsonb NOT NULL, calls integer NOT NULL,
		created_at timestamptz NOT NULL DEFAULT NOW())`).Error; err != nil {
		return nil, err
	}
	return &syntheticInboxProvider{db: private}, nil
}

func (p *syntheticInboxProvider) Execute(ctx context.Context, endpoint inboxintegration.Endpoint, mode, credential string, body []byte, id string) (inboxintegration.Response, error) {
	if (endpoint.Key != "synthetic_once" && endpoint.Key != "synthetic_lost" && endpoint.Key != "synthetic_crash") || id == "" || len(body) > inboxintegration.MaxBody {
		return inboxintegration.Response{}, inboxintegration.Failure{Category: "synthetic_contract_invalid"}
	}
	digest := sha256.Sum256(append(append(append([]byte(endpoint.Key+"\x00"+mode+"\x00"), []byte(credential)...), 0), body...))
	fingerprint := hex.EncodeToString(digest[:])
	result, err := json.Marshal(map[string]any{"reference": "SYNTHETIC-" + id, "count": 0, "active": false})
	if err != nil {
		return inboxintegration.Response{}, err
	}
	first := false
	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inserted := tx.Exec(`INSERT INTO whatomate_synthetic_provider.operations(operation_key,endpoint_key,payload_digest,result,calls)
			VALUES(?,?,?,?::jsonb,1) ON CONFLICT(operation_key) DO NOTHING`, id, endpoint.Key, fingerprint, string(result))
		if inserted.Error != nil {
			return inserted.Error
		}
		first = inserted.RowsAffected == 1
		if first {
			return nil
		}
		var existing struct {
			EndpointKey   string
			PayloadDigest string
			Result        json.RawMessage
		}
		if err := tx.Raw(`SELECT endpoint_key,payload_digest,result FROM whatomate_synthetic_provider.operations WHERE operation_key=? FOR UPDATE`, id).Scan(&existing).Error; err != nil {
			return err
		}
		if existing.EndpointKey != endpoint.Key || existing.PayloadDigest != fingerprint {
			return inboxintegration.Failure{Category: "synthetic_idempotency_conflict"}
		}
		result = append([]byte{}, existing.Result...)
		return tx.Exec("UPDATE whatomate_synthetic_provider.operations SET calls=calls+1 WHERE operation_key=?", id).Error
	})
	if err != nil {
		return inboxintegration.Response{}, err
	}
	// The provider committed its object before this response was lost. The
	// application must retain uncertainty until an explicitly authorised retry.
	if first && endpoint.Key == "synthetic_lost" {
		return inboxintegration.Response{}, inboxintegration.Failure{Category: "synthetic_response_lost", Ambiguous: true}
	}
	if first && endpoint.Key == "synthetic_crash" {
		// The runner crashes the owned HTTP process while this call is still
		// in flight. No application receipt has been recorded yet.
		<-ctx.Done()
		return inboxintegration.Response{}, inboxintegration.Failure{Category: "synthetic_interrupted", Ambiguous: true}
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(result, &fields); err != nil {
		return inboxintegration.Response{}, err
	}
	return inboxintegration.Response{Status: http.StatusCreated, Fields: fields, Duration: time.Millisecond}, nil
}

type syntheticRejectExternal struct{}

func (syntheticRejectExternal) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("external HTTP is disabled in this synthetic test host")
}

// Start only after the ordinary disposable server has migrated and bootstrapped
// this DB. The runner intentionally kills/replaces this test process between
// acceptance phases, keeping PostgreSQL/Redis and the provider ledger intact.
func TestInboxProviderBrowserHost(t *testing.T) {
	if os.Getenv("INBOX_PROVIDER_TEST_MODE") != "synthetic-owned" {
		t.Skip("Explicit owned fake-provider HTTP acceptance host only")
	}
	path := os.Getenv("INBOX_PROVIDER_TEST_CONFIG")
	require.True(t, filepath.IsAbs(path))
	require.Equal(t, "disposable-fork.toml", filepath.Base(path))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, "test", cfg.App.Environment)
	require.True(t, strings.HasPrefix(cfg.Database.Host, "whatomate-inbox-") && cfg.Database.Name == "fixture", "Only the named owned disposable database is permitted")
	require.True(t, cfg.Channels.StopDispatch)
	require.False(t, cfg.InboxAutomation.DispatchEnabled)
	require.False(t, cfg.InboxAutomation.IntegrationDispatchEnabled)
	require.GreaterOrEqual(t, len(cfg.App.EncryptionKey), 32)
	require.GreaterOrEqual(t, len(cfg.JWT.Secret), 32)
	require.Equal(t, "0.0.0.0", cfg.Server.Host)
	require.Equal(t, 8080, cfg.Server.Port)
	// This switch exists only in this test process and can reach only the
	// injected provider. It does not change the on-disk held configuration.
	cfg.InboxAutomation.IntegrationDispatchEnabled = true
	cfg.InboxAutomation.IntegrationEndpoints = `[{"key":"synthetic_once","name":"Synthetic durable service","url":"https://service.example.invalid/once","method":"POST","idempotency_header":"Idempotency-Key","idempotency_retention_seconds":3600},{"key":"synthetic_lost","name":"Synthetic response-loss service","url":"https://service.example.invalid/lost","method":"POST","idempotency_header":"Idempotency-Key","idempotency_retention_seconds":3600},{"key":"synthetic_crash","name":"Synthetic interrupted service","url":"https://service.example.invalid/crash","method":"POST","idempotency_header":"Idempotency-Key","idempotency_retention_seconds":3600}]`
	db, err := database.NewPostgres(&cfg.Database, false)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	rdb, err := database.NewRedis(&cfg.Redis)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	provider, err := newSyntheticInboxProvider(db)
	require.NoError(t, err)
	lo := testutil.NopLogger()
	hub := websocket.NewHub(lo)
	go hub.Run()
	app := &handlers.App{
		Config: cfg, DB: db, Redis: rdb, Log: lo, WSHub: hub,
		Queue: queue.NewRedisQueue(rdb, lo), Assigner: assignment.New(db, rdb, lo),
		HTTPClient:               &http.Client{Transport: syntheticRejectExternal{}},
		WhatsApp:                 whatsapp.NewWithBaseURL(lo, "http://127.0.0.1:1"),
		InboxIntegrationExecutor: provider,
	}
	require.NoError(t, app.InitializeChannels())
	g := fastglue.NewGlue()
	g.Before(middleware.SecurityHeaders())
	g.Before(middleware.RequestLogger(lo))
	g.Before(middleware.Recovery(lo))
	g.Before(middleware.CSRFProtection())
	setupRoutes(g, app, lo, cfg.Server.BasePath, rdb, cfg)
	server := newHTTPServer(corsWrapper(g.Handler(), middleware.ParseAllowedOrigins(cfg.Server.AllowedOrigins)), cfg.Server)
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Shutdown()) })
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); app.RunInboxWorkflows(ctx, 100*time.Millisecond) }()
	}
	t.Cleanup(func() { cancel(); workers.Wait(); app.WaitForBackgroundTasks() })
	t.Log("Owned synthetic provider HTTP host ready; four normal workflow workers; customer messaging held")
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(12 * time.Minute):
		t.Fatal("Owned acceptance host lifetime exceeded; the runner must retire it after testing")
	}
}
