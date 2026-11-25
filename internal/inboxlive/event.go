// Package inboxlive contains the private database invalidation protocol. These
// events are not public WebSocket DTOs: delivery must apply current scope and
// authority first and expose only the watch's opaque invalidation metadata.
package inboxlive

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/google/uuid"
)

const Channel = "whatomate_inbox_live_v1"

var ErrInvalidEvent = errors.New("invalid inbox invalidation event")

type Scope struct {
	AccountID  *uuid.UUID `json:"account_id,omitempty"`
	ContactID  *uuid.UUID `json:"contact_id,omitempty"`
	TeamID     *uuid.UUID `json:"team_id,omitempty"`
	AssigneeID *uuid.UUID `json:"assignee_id,omitempty"`
}

type Event struct {
	Protocol       int       `json:"v"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Kind           string    `json:"kind"`
	ResourceID     uuid.UUID `json:"resource_id"`
	Version        int64     `json:"version"`
	Before         *Scope    `json:"before"`
	After          *Scope    `json:"after"`
}

// Decode accepts only the bounded identifier-only database contract. Invalid
// events require resynchronisation; their raw payload must never be logged.
func Decode(payload []byte) (Event, error) {
	var event Event
	if len(payload) == 0 || len(payload) > 1024 {
		return event, ErrInvalidEvent
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF || event.Protocol != 1 || event.OrganizationID == uuid.Nil || event.ResourceID == uuid.Nil || event.Version < 1 {
		return Event{}, ErrInvalidEvent
	}
	switch event.Kind {
	case "case", "contact", "configuration", "workload", "access":
	default:
		return Event{}, ErrInvalidEvent
	}
	for _, scope := range []*Scope{event.Before, event.After} {
		if scope == nil {
			continue
		}
		for _, id := range []*uuid.UUID{scope.AccountID, scope.ContactID, scope.TeamID, scope.AssigneeID} {
			if id != nil && *id == uuid.Nil {
				return Event{}, ErrInvalidEvent
			}
		}
	}
	return event, nil
}
