package inboxlive

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPostgresListenerCommitBoundaryReconnectAndCredentialRedaction(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	configuration, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	application := "inbox-live-test-" + uuid.NewString()
	configuration.RuntimeParams["application_name"] = application
	publisherConfiguration := configuration.Copy()
	publisherConfiguration.RuntimeParams["application_name"] = "inbox-live-test-publisher"
	publisher, err := pgx.ConnectConfig(context.Background(), publisherConfiguration)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, publisher.Close(context.Background())) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := Listen(ctx, PostgresConnector(configuration), ListenerOptions{RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond})
	connected := func() Update {
		t.Helper()
		deadline, done := context.WithTimeout(ctx, 3*time.Second)
		defer done()
		for {
			select {
			case update, ok := <-updates:
				require.True(t, ok)
				if update.Available && update.Reset && update.Reason == Connected {
					return update
				}
			case <-deadline.Done():
				t.Fatal("owned PostgreSQL listener did not connect")
			}
		}
	}
	firstConnection := connected()
	event := Event{Protocol: 1, OrganizationID: uuid.New(), Kind: "case", ResourceID: uuid.New(), Version: 2}
	data, err := json.Marshal(event)
	require.NoError(t, err)
	tx, err := publisher.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "SELECT pg_notify($1,$2)", Channel, string(data))
	require.NoError(t, err)
	select {
	case update := <-updates:
		require.Nil(t, update.Event, "uncommitted notifications must not be delivered")
	case <-time.After(150 * time.Millisecond):
	}
	require.NoError(t, tx.Rollback(ctx))
	_, err = publisher.Exec(ctx, "SELECT pg_notify($1,$2)", Channel, string(data))
	require.NoError(t, err)
	committed, _ := waitEvent(t, updates)
	require.Equal(t, event, *committed.Event)
	require.Equal(t, firstConnection.Epoch, committed.Epoch)
	// Only the exact newly established connection is selected. Terminating it
	// exercises real reconnect without altering any database role or setting.
	var pid int
	require.NoError(t, publisher.QueryRow(ctx, "SELECT pid FROM pg_stat_activity WHERE application_name=$1", application).Scan(&pid))
	var terminated bool
	require.NoError(t, publisher.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated))
	require.True(t, terminated)
	secondConnection := connected()
	require.NotEqual(t, firstConnection.Epoch, secondConnection.Epoch)
	event.Version = 1 // Same resource after restoring an earlier recovery point.
	data, err = json.Marshal(event)
	require.NoError(t, err)
	_, err = publisher.Exec(ctx, "SELECT pg_notify($1,$2)", Channel, string(data))
	require.NoError(t, err)
	restored, _ := waitEvent(t, updates)
	require.Equal(t, event, *restored.Event)
	require.Equal(t, secondConnection.Epoch, restored.Epoch)
	stopListener(t, cancel, updates)
	bad := configuration.Copy()
	bad.Password = "synthetic-secret-not-for-errors"
	failureCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	source, err := PostgresConnector(bad)(failureCtx)
	require.Nil(t, source)
	require.Error(t, err)
	require.NotContains(t, err.Error(), bad.Password)
	require.NotContains(t, err.Error(), dsn)
}
