package extensions

import (
	"context"
	"fmt"
	"sync"
)

// Actor is constructed by trusted host authentication, never decoded from a body.
// Infrastructure or organization administration does not imply case permission.
type Actor struct {
	OrganizationID string
	UserID         string
	Permissions    []string
	SessionID      string
	MFA            bool
	MFAGeneration  uint64
	// Trusted host policy only; this is not MFA proof or a token claim.
	PrimarySessionAuthorized bool
}

func (a Actor) HasProtectedAuthentication() bool { return a.MFA || a.PrimarySessionAuthorized }

func (a Actor) Has(permission string) bool {
	for _, p := range a.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

// Protection is immutable outside this package. Persist these fields separately
// from editable message metadata. Missing policy always denies protected reads.
type Protection struct{ org, module, resource, classification string }

func NewProtection(org, module, resource, classification string) (Protection, error) {
	if org == "" || !identifier.MatchString(module) || resource == "" || classification == "" {
		return Protection{}, fmt.Errorf("invalid protection")
	}
	return Protection{org, module, resource, classification}, nil
}
func (p Protection) OrganizationID() string { return p.org }
func (p Protection) ModuleID() string       { return p.module }
func (p Protection) ResourceID() string     { return p.resource }
func (p Protection) Classification() string { return p.classification }

type Policy func(context.Context, Actor, Protection, string) error

// Inbound deliberately carries no message body, attachment or profile name.
type Inbound struct{ OrganizationID, AccountID, ProviderMessageID, Sender, Kind string }
type InboundPolicy func(context.Context, Inbound) error
type Guard struct {
	mu       sync.RWMutex
	policies map[string]Policy
	inbound  map[string]InboundPolicy
}

func NewGuard() *Guard {
	return &Guard{policies: map[string]Policy{}, inbound: map[string]InboundPolicy{}}
}
func (g *Guard) Register(id string, p Policy) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !identifier.MatchString(id) || p == nil || g.policies[id] != nil {
		return fmt.Errorf("invalid/duplicate policy")
	}
	g.policies[id] = p
	return nil
}
func (g *Guard) RegisterInbound(id string, p InboundPolicy) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !identifier.MatchString(id) || p == nil || g.inbound[id] != nil {
		return fmt.Errorf("invalid/duplicate inbound policy")
	}
	g.inbound[id] = p
	return nil
}
func (g *Guard) Authorize(ctx context.Context, a Actor, p Protection, action string) (result error) {
	defer func() {
		if recover() != nil {
			result = ErrDenied
		}
	}()
	if a.OrganizationID == "" || a.UserID == "" || a.OrganizationID != p.org || action == "" {
		return ErrDenied
	}
	g.mu.RLock()
	policy := g.policies[p.module]
	g.mu.RUnlock()
	if policy == nil {
		return ErrDenied
	}
	if err := policy(ctx, a, p, action); err != nil {
		return ErrDenied
	}
	return nil
}
func (g *Guard) CheckInbound(ctx context.Context, in Inbound, required []string) (result error) {
	defer func() {
		if recover() != nil {
			result = ErrDenied
		}
	}()
	if len(required) == 0 {
		return nil
	}
	if in.OrganizationID == "" || in.Sender == "" || in.ProviderMessageID == "" {
		return ErrDenied
	}
	for _, id := range required {
		g.mu.RLock()
		p := g.inbound[id]
		g.mu.RUnlock()
		if p == nil {
			return ErrDenied
		}
		if err := p(ctx, in); err != nil {
			return ErrDenied
		}
	}
	return nil
}
