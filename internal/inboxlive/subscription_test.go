package inboxlive

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionRechecksAuthorityAndPublishesOpaqueVersionsOnly(t *testing.T) {
	now := time.Now()
	var allowed atomic.Bool
	allowed.Store(true)
	var checks atomic.Int32
	frames := []Frame{}
	s := NewSubscription(func(context.Context, WatchScope, *Event) (Decision, error) {
		checks.Add(1)
		return Decision{Allowed: allowed.Load(), Match: true, Authority: "current-role"}, nil
	}, func(frame Frame) bool { frames = append(frames, frame); return true })
	s.Replace(context.Background(), WatchSet{Protocol: 1, Generation: 1, Scopes: []WatchScope{{Kind: "queue"}}}, now)
	update := Update{Epoch: uuid.New(), Available: true, Reset: true}
	s.Deliver(context.Background(), update, now)
	event := Event{OrganizationID: uuid.New(), Kind: "case", ResourceID: uuid.New(), Version: 8}
	update.Reset, update.Event = false, &event
	s.Deliver(context.Background(), update, now)
	require.Len(t, frames, 3)
	change := frames[2].Changes[0]
	require.Len(t, change.Resource, 64)
	require.EqualValues(t, 8, change.Version)
	data, err := json.Marshal(frames)
	require.NoError(t, err)
	for _, secret := range []string{event.OrganizationID.String(), event.ResourceID.String(), "current-role", "assignee", "before", "after"} {
		require.NotContains(t, string(data), secret)
	}
	// Even a duplicate must be freshly authorised, but it creates no new frame.
	s.Deliver(context.Background(), update, now)
	event.Version = 7
	s.Deliver(context.Background(), update, now)
	require.Len(t, frames, 3)
	allowed.Store(false)
	s.Deliver(context.Background(), update, now)
	require.Len(t, frames, 4)
	require.True(t, frames[3].Changes[0].Reset)
	require.False(t, frames[3].Changes[0].Allowed)
	require.True(t, frames[3].Changes[0].Clear)
	require.Empty(t, frames[3].Changes[0].Resource)
	require.EqualValues(t, 6, checks.Load())
}

func TestSubscriptionReconnectRestoredRevisionOverflowAndExpiry(t *testing.T) {
	now := time.Now()
	frames := []Frame{}
	var drop bool
	s := NewSubscription(func(context.Context, WatchScope, *Event) (Decision, error) {
		return Decision{Allowed: true, Match: true, Authority: "role"}, nil
	}, func(frame Frame) bool {
		if drop {
			return false
		}
		frames = append(frames, frame)
		return true
	})
	s.Replace(context.Background(), WatchSet{Protocol: 1, Generation: 1, Scopes: []WatchScope{{Kind: "queue"}}}, now)
	u := Update{Epoch: uuid.New(), Available: true, Reset: true}
	s.Deliver(context.Background(), u, now)
	e := Event{Kind: "case", ResourceID: uuid.New(), Version: 10}
	u.Reset, u.Event = false, &e
	s.Deliver(context.Background(), u, now)
	prior := frames[len(frames)-1].Epoch
	u.Epoch, u.Reset = uuid.New(), true
	s.Deliver(context.Background(), u, now)
	e.Version, u.Reset = 1, false
	s.Deliver(context.Background(), u, now)
	require.NotEqual(t, prior, frames[len(frames)-1].Epoch)
	require.EqualValues(t, 1, frames[len(frames)-1].Changes[0].Version)
	prior = frames[len(frames)-1].Epoch
	drop, e.Version = true, 2
	s.Deliver(context.Background(), u, now)
	drop, e.Version = false, 3
	s.Deliver(context.Background(), u, now)
	require.NotEqual(t, prior, frames[len(frames)-1].Epoch, "a full socket queue forces reconciliation in a new epoch")
	for range versionLimit + 3 {
		e.ResourceID = uuid.New()
		s.Deliver(context.Background(), u, now)
	}
	require.LessOrEqual(t, len(s.versions), versionLimit)
	s.Deliver(context.Background(), u, now.Add(WatchLifetime))
	last := frames[len(frames)-1]
	require.Equal(t, "expired", last.State)
	require.Empty(t, s.watched)
	require.False(t, last.Changes[0].Allowed)
}

func TestSubscriptionContextChangeFencesAuthorisationInFlight(t *testing.T) {
	now := time.Now()
	entered, resume := make(chan struct{}), make(chan struct{})
	var block atomic.Bool
	var mu sync.Mutex
	frames := []Frame{}
	s := NewSubscription(func(_ context.Context, scope WatchScope, event *Event) (Decision, error) {
		if event != nil && block.Load() {
			close(entered)
			<-resume
		}
		return Decision{Allowed: true, Match: true, Authority: scope.Key()}, nil
	}, func(frame Frame) bool { mu.Lock(); defer mu.Unlock(); frames = append(frames, frame); return true })
	s.Replace(context.Background(), WatchSet{Protocol: 1, Generation: 1, Scopes: []WatchScope{{Kind: "queue"}}}, now)
	block.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Deliver(context.Background(), Update{Epoch: uuid.New(), Available: true, Event: &Event{Kind: "case", ResourceID: uuid.New(), Version: 1}}, now)
	}()
	<-entered
	// Unsubscribe must not wait for the old storage lookup or retain its hint.
	s.Replace(context.Background(), WatchSet{Protocol: 1, Generation: 2}, now)
	close(resume)
	<-done
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, frames, 2)
	require.EqualValues(t, 2, frames[1].Generation)
	require.Empty(t, frames[1].Changes)
}

func TestSubscriptionBoundedRenewalAndChangedAuthorityReset(t *testing.T) {
	now := time.Now()
	role := "role-1"
	frames := []Frame{}
	s := NewSubscription(func(context.Context, WatchScope, *Event) (Decision, error) {
		return Decision{Allowed: true, Match: false, Authority: role}, nil
	}, func(frame Frame) bool { frames = append(frames, frame); return true })
	set := WatchSet{Protocol: 1, Generation: 1, Scopes: []WatchScope{{Kind: "queue"}}}
	s.Replace(context.Background(), set, now)
	set.Generation++
	s.Replace(context.Background(), set, now.Add(time.Millisecond))
	require.Equal(t, "rate_limited", frames[len(frames)-1].State)
	require.Empty(t, s.watched)
	set.Generation++
	s.Replace(context.Background(), set, now.Add(WatchInterval))
	u := Update{Epoch: uuid.New(), Available: true, Reset: true}
	s.Deliver(context.Background(), u, now.Add(time.Second))
	role, u.Reset = "role-2", false
	u.Event = &Event{Kind: "case", ResourceID: uuid.New(), Version: 1}
	s.Deliver(context.Background(), u, now.Add(time.Second))
	last := frames[len(frames)-1]
	require.True(t, last.Changes[0].Reset)
	require.True(t, last.Changes[0].Clear, "changed authority cannot preserve old private rows")
	require.Empty(t, last.Changes[0].Resource)
	before := len(frames)
	s.Replace(context.Background(), set, now.Add(time.Second))
	require.Len(t, frames, before, "stale generations cannot replace a current watch")
	data, err := json.Marshal(last)
	require.NoError(t, err)
	require.False(t, strings.Contains(string(data), "role-"))
}

func TestSubscriptionLateReconnectSnapshotCannotReplaceNewerListenerEpoch(t *testing.T) {
	now := time.Now()
	frames := []Frame{}
	s := NewSubscription(func(context.Context, WatchScope, *Event) (Decision, error) {
		return Decision{Allowed: true, Match: true, Authority: "current"}, nil
	}, func(frame Frame) bool { frames = append(frames, frame); return true })
	s.Replace(context.Background(), WatchSet{Protocol: 1, Generation: 1, Scopes: []WatchScope{{Kind: "queue"}}}, now)
	newer := Update{Order: 2, Epoch: uuid.New(), Available: true, Reset: true}
	s.Deliver(context.Background(), newer, now)
	accepted := frames[len(frames)-1].Epoch
	before := len(frames)
	older := Update{Order: 1, Epoch: uuid.New(), Available: false, Reset: true}
	s.Deliver(context.Background(), older, now)
	require.Len(t, frames, before)
	require.Equal(t, accepted, s.epoch)
	require.True(t, s.available)
	newer.Order, newer.Reset = 3, false
	newer.Event = &Event{Kind: "case", ResourceID: uuid.New(), Version: 1}
	s.Deliver(context.Background(), newer, now)
	require.Equal(t, accepted, frames[len(frames)-1].Epoch)
	require.EqualValues(t, 1, frames[len(frames)-1].Changes[0].Version)
}
