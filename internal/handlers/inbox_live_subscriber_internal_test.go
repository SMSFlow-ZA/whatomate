package handlers

import (
	"testing"
	"time"

	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxLiveSubscriberOwnsOneConnectionAndStopsCleanly(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger(), WSHub: ws.NewHub(testutil.NopLogger())}
	t.Cleanup(a.StopInboxLiveSubscriber)
	count := func(expected int64) {
		t.Helper()
		require.Eventually(t, func() bool {
			var value int64
			err := db.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='whatomate-inbox-live'").Scan(&value).Error
			return err == nil && value == expected
		}, 3*time.Second, 10*time.Millisecond)
	}
	count(0)
	require.NoError(t, a.StartInboxLiveSubscriber())
	count(1)
	require.NoError(t, a.StartInboxLiveSubscriber())
	count(1)
	a.StopInboxLiveSubscriber()
	count(0)
	a.WaitForBackgroundTasks()
	a.StopInboxLiveSubscriber()
	require.NoError(t, a.StartInboxLiveSubscriber())
	count(1)
	a.StopInboxLiveSubscriber()
	count(0)
	var missing App
	require.EqualError(t, missing.StartInboxLiveSubscriber(), "inbox live dependencies unavailable")
}
