package extensions

import (
	"context"
	"encoding/json"
	"time"
)

// Scope is supplied by host configuration and authentication, not module input.
type Scope struct{ OrganizationID, ModuleID string }
type Record struct {
	Key     string
	Version uint64
	Data    json.RawMessage
}

// Store binds organization/module namespace before returning a transaction.
// ExpectedVersion=0 means insert only. Transaction success includes audit and
// outbox persistence; unknown commit outcome must not be reported as success.
type Store interface {
	Transact(context.Context, Scope, func(Tx) error) error
}
type Tx interface {
	Get(context.Context, string) (Record, error)
	Put(context.Context, Record, uint64) error
	Delete(context.Context, string, uint64) error
	Audit(context.Context, AuditEvent) error
	Enqueue(context.Context, Job) error
}
type AuditEvent struct {
	Action, ResourceID, ActorID, ReasonCode string
	At                                      time.Time
}
type Job struct {
	ID, Kind, ResourceID string
	Payload              json.RawMessage
	NotBefore            time.Time
}

// Messaging must recheck current eligibility, suppression and resource access
// at dispatch, not just when the job was created. Unknown provider outcome is
// held for reconciliation and never automatically resent or sent over fallback.
type Message struct {
	IdempotencyKey, RecipientID, ResourceID, TemplateID string
	Parameters                                          map[string]string
}
type Messaging interface {
	Enqueue(context.Context, Scope, Actor, Message) error
}
type Migration struct {
	ID, Checksum string
	Up           func(context.Context, Store) error
}
type Worker interface{ Run(context.Context) error }
type Module interface {
	Descriptor() Descriptor
	Migrations() []Migration
	Workers() []Worker
}
type FlowInput struct {
	Actor      Actor
	ResourceID string
	Variables  map[string]string
}
type FlowOutput struct {
	Next      string
	Variables map[string]string
}
type FlowNode func(context.Context, FlowInput) (FlowOutput, error)
