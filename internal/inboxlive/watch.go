package inboxlive

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

const MaxWatchScopes = 3

var ErrInvalidWatch = errors.New("invalid inbox watch")

// WatchScope describes client-known context only. Validation checks shape;
// current database authority and owned identifiers must be checked separately.
type WatchScope struct {
	Kind      string     `json:"kind"`
	AccountID *uuid.UUID `json:"account_id,omitempty"`
	TeamID    *uuid.UUID `json:"team_id,omitempty"`
	CaseID    *uuid.UUID `json:"case_id,omitempty"`
	ContactID *uuid.UUID `json:"contact_id,omitempty"`
}

type WatchSet struct {
	Protocol   int          `json:"v"`
	Generation uint32       `json:"generation"`
	Scopes     []WatchScope `json:"scopes"`
}

func (scope WatchScope) Validate() error {
	for _, id := range []*uuid.UUID{scope.AccountID, scope.TeamID, scope.CaseID, scope.ContactID} {
		if id != nil && *id == uuid.Nil {
			return ErrInvalidWatch
		}
	}
	switch scope.Kind {
	case "case":
		if scope.CaseID == nil || scope.AccountID != nil || scope.TeamID != nil || scope.ContactID != nil {
			return ErrInvalidWatch
		}
	case "conversation":
		if scope.AccountID == nil || scope.ContactID == nil || scope.TeamID != nil || scope.CaseID != nil {
			return ErrInvalidWatch
		}
	case "contact":
		if scope.ContactID == nil || scope.AccountID != nil || scope.TeamID != nil || scope.CaseID != nil {
			return ErrInvalidWatch
		}
	case "capacity":
		if scope.AccountID == nil || scope.TeamID == nil || scope.CaseID != nil || scope.ContactID != nil {
			return ErrInvalidWatch
		}
	case "queue", "workload", "supervisor":
		if scope.CaseID != nil || scope.ContactID != nil {
			return ErrInvalidWatch
		}
	default:
		return ErrInvalidWatch
	}
	return nil
}

// Key uses only the validated identifiers the client selected. It carries no
// server-discovered account, team, contact, assignee or case information.
func (scope WatchScope) Key() string {
	parts := []string{scope.Kind}
	for _, id := range []*uuid.UUID{scope.AccountID, scope.TeamID, scope.CaseID, scope.ContactID} {
		value := ""
		if id != nil {
			value = id.String()
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "/")
}

// DecodeWatch preserves the existing ordinary WebSocket frame bound. Empty
// scopes explicitly unsubscribe. No organisation/user/permission field is
// accepted from the client; those come from the authenticated socket identity.
func DecodeWatch(payload []byte) (WatchSet, error) {
	var set WatchSet
	if len(payload) == 0 || len(payload) > 512 {
		return set, ErrInvalidWatch
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&set) != nil || decoder.Decode(new(any)) != io.EOF || set.Protocol != 1 || set.Generation == 0 || len(set.Scopes) > MaxWatchScopes {
		return WatchSet{}, ErrInvalidWatch
	}
	seen := make(map[string]bool, len(set.Scopes))
	for _, scope := range set.Scopes {
		if scope.Validate() != nil || seen[scope.Key()] {
			return WatchSet{}, ErrInvalidWatch
		}
		seen[scope.Key()] = true
	}
	return set, nil
}
