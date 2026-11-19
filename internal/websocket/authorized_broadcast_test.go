package websocket_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/stretchr/testify/require"
)

func TestHub_AuthorizedDeliveryRespectsRevocationAndOrganizations(t *testing.T) {
	hub := newTestHub(t)
	org, user, denied := uuid.New(), uuid.New(), uuid.New()
	allowedClient := newTestClient(hub, user, org)
	deniedClient := newTestClient(hub, denied, org)
	otherOrg := newTestClient(hub, user, uuid.New())
	for _, c := range []*websocket.Client{allowedClient, deniedClient, otherOrg} {
		hub.Register(c)
	}
	waitForClientCount(t, hub, 3)
	var allowed atomic.Bool
	allowed.Store(true)
	checked := make(chan struct{}, 4)
	msg := websocket.BroadcastMessage{OrgID: org, Message: websocket.WSMessage{Type: "private"},
		Authorize: func(id uuid.UUID) bool {
			// Taking the hub's lock here must not deadlock delivery.
			require.Equal(t, 3, hub.GetClientCount())
			checked <- struct{}{}
			return id == user && allowed.Load()
		}}
	hub.Broadcast(msg)
	select {
	case <-websocket.ClientSendChan(allowedClient):
	case <-time.After(time.Second):
		t.Fatal("authorized client received no event")
	}
	for range 2 {
		<-checked
	}
	require.Empty(t, websocket.ClientSendChan(deniedClient))
	require.Empty(t, websocket.ClientSendChan(otherOrg))
	allowed.Store(false)
	hub.Broadcast(msg)
	for range 2 {
		select {
		case <-checked:
		case <-time.After(time.Second):
			t.Fatal("authorization did not run")
		}
	}
	require.Empty(t, websocket.ClientSendChan(allowedClient))
}
