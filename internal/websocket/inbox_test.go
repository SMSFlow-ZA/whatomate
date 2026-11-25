package websocket_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/stretchr/testify/require"
)

func inboxFrame(t *testing.T, c *websocket.Client) inboxlive.Frame {
	t.Helper()
	select {
	case data := <-c.SendChan():
		var result struct {
			Type    string
			Payload inboxlive.Frame
		}
		require.NoError(t, json.Unmarshal(data, &result))
		require.Equal(t, websocket.TypeInboxState, result.Type)
		return result.Payload
	case <-time.After(time.Second):
		t.Fatal("no inbox frame")
		return inboxlive.Frame{}
	}
}

func TestInboxWatchHubScopesOrganisationsAndAuthorisesOutsideRegistryLock(t *testing.T) {
	hub := newTestHub(t)
	org := uuid.New()
	client := newTestClient(hub, uuid.New(), org)
	foreign := newTestClient(hub, uuid.New(), uuid.New())
	var checks atomic.Int32
	authorize := func(context.Context, inboxlive.WatchScope, *inboxlive.Event) (inboxlive.Decision, error) {
		checks.Add(1)
		require.Equal(t, 2, hub.GetClientCount(), "authorisation cannot hold the registry lock")
		return inboxlive.Decision{Allowed: true, Match: true, Authority: "current"}, nil
	}
	client.SetInboxAuthorizer(authorize)
	foreign.SetInboxAuthorizer(authorize)
	hub.Register(client)
	hub.Register(foreign)
	waitForClientCount(t, hub, 2)
	for _, c := range []*websocket.Client{client, foreign} {
		websocket.ClientHandleInboxWatch(c, inboxlive.WatchSet{Protocol: 1, Generation: 1, Scopes: []inboxlive.WatchScope{{Kind: "queue"}}})
		require.Equal(t, "watching", inboxFrame(t, c).State)
	}
	u := inboxlive.Update{Epoch: uuid.New(), Available: true, Reset: true}
	hub.DeliverInboxUpdate(context.Background(), u)
	inboxFrame(t, client)
	inboxFrame(t, foreign)
	before := checks.Load()
	u.Reset, u.Event = false, &inboxlive.Event{OrganizationID: org, Kind: "case", ResourceID: uuid.New(), Version: 3}
	hub.DeliverInboxUpdate(context.Background(), u)
	frame := inboxFrame(t, client)
	require.EqualValues(t, 3, frame.Changes[0].Version)
	require.EqualValues(t, before+1, checks.Load())
	require.Empty(t, foreign.SendChan())
	// Disconnect may close send while a listener snapshot is in flight.
	hub.Unregister(client)
	waitForClientCount(t, hub, 1)
	hub.DeliverInboxUpdate(context.Background(), u)
	require.Empty(t, foreign.SendChan())
}

func TestInboxWatchRejectsUnauthenticatedAndForgedPayloads(t *testing.T) {
	hub := newTestHub(t)
	var checks atomic.Int32
	client := websocket.NewUnauthenticatedClient(hub, nil, nil)
	client.SetInboxAuthorizer(func(context.Context, inboxlive.WatchScope, *inboxlive.Event) (inboxlive.Decision, error) {
		checks.Add(1)
		return inboxlive.Decision{Allowed: true}, nil
	})
	websocket.ClientHandleInboxWatch(client, inboxlive.WatchSet{Protocol: 1, Generation: 1, Scopes: []inboxlive.WatchScope{{Kind: "queue"}}})
	require.Zero(t, checks.Load())
	require.Empty(t, client.SendChan())
	client = newTestClient(hub, uuid.New(), uuid.New())
	client.SetInboxAuthorizer(func(context.Context, inboxlive.WatchScope, *inboxlive.Event) (inboxlive.Decision, error) {
		checks.Add(1)
		return inboxlive.Decision{Allowed: true}, nil
	})
	websocket.ClientHandleInboxWatch(client, map[string]any{"v": 1, "generation": 1, "scopes": []any{map[string]any{"kind": "queue", "is_super_admin": true}}})
	require.Zero(t, checks.Load())
	require.Empty(t, client.SendChan())
}
