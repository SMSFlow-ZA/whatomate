package websocket

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
)

// SetInboxAuthorizer must be called before either socket pump starts. It does
// not grant access or enable watches before successful socket authentication.
func (c *Client) SetInboxAuthorizer(authorize inboxlive.Authorizer) {
	c.inbox = inboxlive.NewSubscription(authorize, func(frame inboxlive.Frame) bool {
		data, err := json.Marshal(WSMessage{Type: TypeInboxState, Payload: frame})
		if err != nil {
			return false
		}
		c.hub.mu.RLock()
		defer c.hub.mu.RUnlock()
		if c.closed {
			return false
		}
		select {
		case c.send <- data:
			return true
		default:
			return false
		}
	})
}

func (c *Client) handleInboxWatch(payload any) {
	if !c.authenticated || c.inbox == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	set, err := inboxlive.DecodeWatch(data)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.inbox.Replace(ctx, set, time.Now())
	c.hub.mu.RLock()
	state := c.hub.inboxState
	c.hub.mu.RUnlock()
	if state.Epoch != uuid.Nil {
		c.inbox.Deliver(ctx, state, time.Now())
	}
}

// DeliverInboxUpdate runs on the listener's dedicated path, never Hub.Run.
// Database authorisation happens after releasing the client registry lock.
// Four bounded workers prevent a slow lookup from blocking ordinary messages.
func (h *Hub) DeliverInboxUpdate(ctx context.Context, update inboxlive.Update) {
	h.mu.Lock()
	update.Order = h.inboxState.Order + 1
	h.inboxState = inboxlive.Update{Order: update.Order, Epoch: update.Epoch, Available: update.Available, Reset: true, Reason: update.Reason}
	targets := make([]*inboxlive.Subscription, 0)
	for org, users := range h.clients {
		if update.Event != nil && update.Event.OrganizationID != org {
			continue
		}
		for _, clients := range users {
			for c := range clients {
				if c.inbox != nil {
					targets = append(targets, c.inbox)
				}
			}
		}
	}
	h.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	jobs := make(chan *inboxlive.Subscription)
	var workers sync.WaitGroup
	for range min(4, len(targets)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for subscription := range jobs {
				lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				subscription.Deliver(lookupCtx, update, time.Now())
				cancel()
			}
		}()
	}
	for _, target := range targets {
		select {
		case jobs <- target:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}
