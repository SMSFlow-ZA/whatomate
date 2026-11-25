package inboxlive

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeSource struct {
	payloads [][]byte
	index    int
	gate     <-chan struct{}
	end      error
	drained  chan struct{}
	once     sync.Once
	closed   atomic.Int32
}

func (source *fakeSource) Wait(ctx context.Context) ([]byte, error) {
	if source.index < len(source.payloads) {
		payload := source.payloads[source.index]
		source.index++
		return payload, nil
	}
	if source.drained != nil {
		source.once.Do(func() { close(source.drained) })
	}
	if source.gate != nil {
		select {
		case <-source.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if source.end != nil {
		return nil, source.end
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (source *fakeSource) Close(context.Context) error {
	source.closed.Add(1)
	return nil
}

func testEvent(t *testing.T, version int64) []byte {
	t.Helper()
	data, err := json.Marshal(Event{Protocol: 1, OrganizationID: uuid.New(), Kind: "case", ResourceID: uuid.New(), Version: version})
	require.NoError(t, err)
	return data
}

func waitEvent(t *testing.T, updates <-chan Update) (Update, []Update) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var states []Update
	for {
		select {
		case update, ok := <-updates:
			require.True(t, ok, "listener closed before expected event")
			if update.Event != nil {
				return update, states
			}
			states = append(states, update)
		case <-ctx.Done():
			t.Fatal("listener did not return an expected event")
		}
	}
}

func stopListener(t *testing.T, cancel context.CancelFunc, updates <-chan Update) {
	t.Helper()
	cancel()
	ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	for {
		select {
		case _, open := <-updates:
			if !open {
				return
			}
		case <-ctx.Done():
			t.Fatal("listener did not stop after cancellation")
		}
	}
}

func TestListenerRetriesConnectionWithoutPublishingErrorDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeSource{payloads: [][]byte{testEvent(t, 3)}}
	var attempts atomic.Int32
	updates := Listen(ctx, func(context.Context) (Source, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("synthetic-secret-in-connection-error")
		}
		return source, nil
	}, ListenerOptions{RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond})
	update, states := waitEvent(t, updates)
	require.EqualValues(t, 2, attempts.Load())
	require.EqualValues(t, 3, update.Event.Version)
	require.True(t, update.Available)
	require.NotEqual(t, uuid.Nil, update.Epoch)
	var connected bool
	for _, state := range states {
		require.True(t, state.Reset)
		if state.Reason == Connected {
			connected = true
			require.Equal(t, update.Epoch, state.Epoch)
		}
		encoded, err := json.Marshal(state)
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(encoded), "internal state is not a public frame")
	}
	require.True(t, connected)
	stopListener(t, cancel, updates)
	require.EqualValues(t, 1, source.closed.Load())
}

func TestListenerReconnectAcceptsRestoredLowerRevisionsInNewEpoch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := make(chan struct{})
	original := testEvent(t, 9)
	var restored Event
	require.NoError(t, json.Unmarshal(original, &restored))
	restored.Version = 1
	restoredPayload, err := json.Marshal(restored)
	require.NoError(t, err)
	first := &fakeSource{payloads: [][]byte{original}, gate: gate, end: errors.New("synthetic-private-read-error")}
	second := &fakeSource{payloads: [][]byte{restoredPayload}}
	var attempts atomic.Int32
	updates := Listen(ctx, func(context.Context) (Source, error) {
		if attempts.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}, ListenerOptions{RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond})
	before, _ := waitEvent(t, updates)
	require.EqualValues(t, 9, before.Event.Version)
	close(gate)
	after, states := waitEvent(t, updates)
	require.EqualValues(t, 1, after.Event.Version)
	require.Equal(t, before.Event.ResourceID, after.Event.ResourceID)
	require.NotEqual(t, before.Epoch, after.Epoch)
	var reset bool
	for _, state := range states {
		if state.Reset && state.Available && state.Epoch == after.Epoch {
			reset = true
		}
	}
	require.True(t, reset, "a new epoch requires authoritative reload before lower restored revisions")
	stopListener(t, cancel, updates)
	require.EqualValues(t, 1, first.closed.Load())
	require.EqualValues(t, 1, second.closed.Load())
}

func TestListenerOverflowDiscardsHintsAndKeepsResynchronisation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeSource{drained: make(chan struct{})}
	for i := 1; i <= UpdateBuffer*3+10; i++ {
		source.payloads = append(source.payloads, testEvent(t, int64(i)))
	}
	updates := Listen(ctx, func(context.Context) (Source, error) { return source, nil }, ListenerOptions{})
	select {
	case <-source.drained:
	case <-time.After(2 * time.Second):
		t.Fatal("bounded producer blocked on an unread queue")
	}
	require.LessOrEqual(t, len(updates), UpdateBuffer)
	first := <-updates
	require.True(t, first.Reset)
	require.Equal(t, Overflow, first.Reason)
	require.True(t, first.Available)
	for len(updates) > 0 {
		event := <-updates
		require.Equal(t, first.Epoch, event.Epoch)
		require.NotNil(t, event.Event)
	}
	stopListener(t, cancel, updates)
	require.EqualValues(t, 1, source.closed.Load())
}

func TestListenerInvalidPayloadForcesResetWithoutDispatchingIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeSource{payloads: [][]byte{[]byte(`{"secret":"synthetic-private-value"}`), testEvent(t, 2)}}
	updates := Listen(ctx, func(context.Context) (Source, error) { return source, nil }, ListenerOptions{})
	event, states := waitEvent(t, updates)
	require.EqualValues(t, 2, event.Event.Version)
	var invalid bool
	for _, state := range states {
		require.Nil(t, state.Event)
		require.NotContains(t, string(state.Reason), "synthetic")
		if state.Reason == InvalidEvent {
			invalid = true
			require.Equal(t, event.Epoch, state.Epoch)
		}
	}
	require.True(t, invalid)
	stopListener(t, cancel, updates)
}
