package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
)

// canReceiveConversation uses the same current account/assignment boundary as
// HTTP reads. An account-less event requires genuine contact-level access.
func (a *App) canReceiveConversation(org, user, contact uuid.UUID, account string) bool {
	if !a.HasPermission(user, models.ResourceChat, models.ActionRead, org) {
		return false
	}
	_, _, err := a.findInboxChatContact(org, user, contact, account)
	return err == nil
}

func (a *App) broadcastConversation(org, contact uuid.UUID, account string, message websocket.WSMessage) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.Broadcast(websocket.BroadcastMessage{OrgID: org, Message: message, Authorize: func(user uuid.UUID) bool {
		return a.canReceiveConversation(org, user, contact, account)
	}})
}

func (a *App) broadcastResource(org uuid.UUID, resource string, message websocket.WSMessage) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.Broadcast(websocket.BroadcastMessage{OrgID: org, Message: message, Authorize: func(user uuid.UUID) bool {
		return a.HasPermission(user, resource, models.ActionRead, org)
	}})
}
