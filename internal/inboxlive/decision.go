package inboxlive

import "context"

// Decision never contains database content. Authority is an internal fingerprint
// used to discard prior-context hints when current permissions change.
type Decision struct {
	Allowed   bool
	Match     bool
	Reset     bool
	Clear     bool
	Authority string
}

// Authorizer must load current authority for both subscription (nil event) and
// delivery. It runs outside the socket registry lock with a bounded context.
type Authorizer func(context.Context, WatchScope, *Event) (Decision, error)
