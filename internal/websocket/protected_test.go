package websocket

import (
	"github.com/google/uuid"
	"github.com/zerodha/logf"
	"testing"
)

func TestDeliveryGuardChecksEveryRecipientAndCurrentState(t *testing.T) {
	h := NewHub(logf.New(logf.Opts{}))
	org, user := uuid.New(), uuid.New()
	c := &Client{organizationID: org, userID: user, send: make(chan []byte, 2)}
	h.clients[org] = map[uuid.UUID]map[*Client]struct{}{user: {c: {}}}
	permitted := false
	calls := 0
	h.SetDeliveryGuard(func(orgID, userID uuid.UUID, msg WSMessage) bool { calls++; return permitted })
	event := BroadcastMessage{OrgID: org, UserID: user, Message: WSMessage{Type: TypeNewMessage, Payload: "SECRET-MARKER"}}
	h.broadcastMessage(event)
	if len(c.send) != 0 {
		t.Fatal("denied marker delivered")
	}
	permitted = true
	h.broadcastMessage(event)
	if len(c.send) != 1 {
		t.Fatal("authorized delivery absent")
	}
	permitted = false
	h.broadcastMessage(event)
	if len(c.send) != 1 || calls != 3 {
		t.Fatal("revoked recipient received marker")
	}
}

func TestDeliveryGuardRejectsRevokedClientAndPanics(t *testing.T) {
	h := NewHub(logf.New(logf.Opts{}))
	org, user := uuid.New(), uuid.New()
	c := &Client{organizationID: org, userID: user, send: make(chan []byte, 2)}
	h.clients[org] = map[uuid.UUID]map[*Client]struct{}{user: {c: {}}}
	current := true
	c.SetDeliveryCheck(func() bool { return current })
	event := BroadcastMessage{OrgID: org, Message: WSMessage{Payload: "SECRET-MARKER"}}
	current = false
	h.broadcastMessage(event)
	if len(c.send) != 0 {
		t.Fatal("revoked socket delivered")
	}
	c.SetDeliveryCheck(func() bool { panic("policy unavailable") })
	h.broadcastMessage(event)
	if len(c.send) != 0 {
		t.Fatal("policy panic delivered")
	}
}

func TestQueuedFrameRechecksRevocation(t *testing.T) {
	current := true
	c := &Client{}
	c.SetDeliveryCheck(func() bool { return current })
	if !c.allowFrame() {
		t.Fatal("current frame denied")
	}
	current = false
	if c.allowFrame() {
		t.Fatal("queued frame bypassed revocation")
	}
}
