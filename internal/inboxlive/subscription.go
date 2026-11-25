package inboxlive

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	WatchLifetime = 70 * time.Second
	WatchInterval = 500 * time.Millisecond
	versionLimit  = 256
)

// Frame is the public invalidation contract. Scope keys contain only the
// client's selected identifiers. Resources are connection-private HMACs; no
// newly discovered case, owner, account, message or field content is published.
type Frame struct {
	Protocol   int           `json:"v"`
	Generation uint32        `json:"generation"`
	Epoch      uuid.UUID     `json:"epoch"`
	Sequence   uint64        `json:"sequence"`
	Available  bool          `json:"available"`
	State      string        `json:"state"`
	Changes    []ScopeChange `json:"changes"`
}

type ScopeChange struct {
	Scope    string `json:"scope"`
	Allowed  bool   `json:"allowed"`
	Reset    bool   `json:"reset"`
	Resource string `json:"resource,omitempty"`
	Version  int64  `json:"version,omitempty"`
}

type watchedScope struct {
	scope     WatchScope
	authority string
}

// Subscription has fixed memory bounds and no goroutine of its own. The send
// callback must be nonblocking and must not call back into this subscription.
// The registry takes its own lock only inside send, never during authorisation.
type Subscription struct {
	mu          sync.Mutex
	dispatchMu  sync.Mutex
	authorize   Authorizer
	send        func(Frame) bool
	secret      []byte
	generation  uint32
	reservation uint64
	watched     []watchedScope
	lastWatch   time.Time
	expires     time.Time
	epoch       uuid.UUID
	upstream    uuid.UUID
	sequence    uint64
	lastOrder   uint64
	available   bool
	versions    map[string]int64
}

func NewSubscription(authorize Authorizer, send func(Frame) bool) *Subscription {
	return &Subscription{authorize: authorize, send: send, secret: []byte(uuid.NewString()), epoch: uuid.New(), versions: make(map[string]int64)}
}

func (s *Subscription) emitLocked(state string, changes []ScopeChange) {
	s.sequence++
	frame := Frame{Protocol: 1, Generation: s.generation, Epoch: s.epoch, Sequence: s.sequence, Available: s.available, State: state, Changes: changes}
	if s.send == nil || !s.send(frame) {
		// A lost frame invalidates all previous revisions. The next successful
		// frame has a new epoch and requests an authoritative context reload.
		s.epoch, s.sequence = uuid.New(), 0
		clear(s.versions)
		s.available = false
	}
}

// Replace fences the old context before any storage lookup. Even rejected or
// rate-limited changes cannot continue receiving the previous watch's hints.
func (s *Subscription) Replace(ctx context.Context, set WatchSet, now time.Time) {
	dataValid := set.Protocol == 1 && set.Generation > 0 && len(set.Scopes) <= MaxWatchScopes
	seen := map[string]bool{}
	for _, scope := range set.Scopes {
		if scope.Validate() != nil || seen[scope.Key()] {
			dataValid = false
		}
		seen[scope.Key()] = true
	}
	if !dataValid {
		return
	}
	s.mu.Lock()
	if set.Generation <= s.generation {
		s.mu.Unlock()
		return
	}
	s.reservation++
	reservation := s.reservation
	s.generation, s.watched, s.expires = set.Generation, nil, time.Time{}
	s.epoch, s.sequence, s.lastOrder = uuid.New(), 0, 0
	clear(s.versions)
	if len(set.Scopes) > 0 && !s.lastWatch.IsZero() && now.Sub(s.lastWatch) < WatchInterval {
		s.emitLocked("rate_limited", nil)
		s.mu.Unlock()
		return
	}
	s.lastWatch = now
	s.mu.Unlock()

	watch := make([]watchedScope, 0, len(set.Scopes))
	changes := make([]ScopeChange, 0, len(set.Scopes))
	for _, scope := range set.Scopes {
		decision := Decision{}
		var err error
		if s.authorize != nil {
			decision, err = s.authorize(ctx, scope, nil)
		}
		allowed := err == nil && decision.Allowed && ctx.Err() == nil
		changes = append(changes, ScopeChange{Scope: scope.Key(), Allowed: allowed, Reset: true})
		if allowed {
			watch = append(watch, watchedScope{scope: scope, authority: decision.Authority})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reservation != reservation {
		return
	}
	s.watched, s.expires = watch, now.Add(WatchLifetime)
	s.emitLocked("watching", changes)
}

// Deliver performs fresh authorisation without holding either subscription or
// registry locks. The reservation fence discards a superseded lookup result.
func (s *Subscription) Deliver(ctx context.Context, update Update, now time.Time) {
	// Reconnect snapshots and listener updates may race. Serialize delivery
	// lookups without preventing Replace from fencing a changed watch immediately.
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	if update.Order != 0 && update.Order <= s.lastOrder {
		s.mu.Unlock()
		return
	}
	if len(s.watched) == 0 {
		s.mu.Unlock()
		return
	}
	if !now.Before(s.expires) {
		changes := make([]ScopeChange, 0, len(s.watched))
		for _, watch := range s.watched {
			changes = append(changes, ScopeChange{Scope: watch.scope.Key(), Reset: true})
		}
		s.watched = nil
		s.reservation++
		s.emitLocked("expired", changes)
		s.mu.Unlock()
		return
	}
	reservation := s.reservation
	watch := append([]watchedScope(nil), s.watched...)
	s.mu.Unlock()

	changes := make([]ScopeChange, 0, len(watch))
	kept := make([]watchedScope, 0, len(watch))
	for _, item := range watch {
		decision, err := s.authorize(ctx, item.scope, update.Event)
		allowed := err == nil && decision.Allowed && ctx.Err() == nil
		change := ScopeChange{Scope: item.scope.Key(), Allowed: allowed, Reset: true}
		if allowed {
			kept = append(kept, watchedScope{scope: item.scope, authority: decision.Authority})
		}
		if !allowed || update.Reset || decision.Authority != item.authority || decision.Reset {
			changes = append(changes, change)
		} else if decision.Match && update.Event != nil {
			mac := hmac.New(sha256.New, s.secret)
			_, _ = mac.Write([]byte(update.Event.Kind + "/" + update.Event.ResourceID.String()))
			change.Resource, change.Version, change.Reset = hex.EncodeToString(mac.Sum(nil)), update.Event.Version, false
			changes = append(changes, change)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reservation != s.reservation {
		return
	}
	if update.Order != 0 {
		s.lastOrder = update.Order
	}
	s.watched = kept
	reset := update.Reset || s.upstream != update.Epoch
	if reset {
		s.upstream, s.epoch, s.sequence = update.Epoch, uuid.New(), 0
		clear(s.versions)
	}
	s.available = update.Available
	filtered := make([]ScopeChange, 0, len(changes))
	for _, change := range changes {
		if reset {
			change.Resource, change.Version, change.Reset = "", 0, true
		}
		if change.Resource != "" {
			key := change.Scope + "/" + change.Resource
			if change.Version <= s.versions[key] {
				continue
			}
			if len(s.versions) >= versionLimit {
				clear(s.versions)
				s.epoch, s.sequence = uuid.New(), 0
				change.Resource, change.Version, change.Reset = "", 0, true
			} else {
				s.versions[key] = change.Version
			}
		}
		filtered = append(filtered, change)
	}
	if len(filtered) > 0 {
		s.emitLocked("update", filtered)
	}
}
