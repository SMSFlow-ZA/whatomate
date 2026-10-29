# Channel provider SDK and durable dispatch foundation

This additive foundation exports `pkg/channels` API version 1 and a provider-neutral
`internal/channelapp` service. No provider module is imported or registered by
this package. Ordinary Whatomate startup, routes and authentication are unchanged.
Issue #601 discusses subsequent host/UI integration before a larger contribution.

The trusted composition root creates a registry, registers compiled providers,
passes a stable encryption key and calls the standard migrations before invoking
`Service.Run`. Connection credentials remain encrypted; API-facing model JSON
omits credentials, polling cursors and destinations. The host must authenticate
all callers, authorize connection management and supply a non-nil initiating user
for external requests. A nil user is reserved for trusted bot jobs. Queued sends
recheck the active identity and organization role. `AuthorizeSend` may add host
policy; `Notify` receives committed invalidations only.

Each logical SMS ID is immutable. A durable one-shot claim precedes the network
call; uncertain results are reconciled rather than resubmitted. Event pages and
their cursor commit together; repeated receipts are deduplicated. Marketing and
SMS opt-outs are checked again at dispatch. Author-written fallback is eligible
only after confirmed recipient-undeliverable WhatsApp failure; timeouts alone do
not authorize a second delivery channel. Balance snapshots retain their advertised
units and never calculate billing or provision accounts.

`channelmigration.Run` only relaxes the existing campaign template column to
allow an SMS campaign; existing WhatsApp references remain. Channel fields default
to WhatsApp. Existing installations receive additive schema only and retain WhatsApp behavior until a trusted
host deliberately composes a provider and invokes the service. Full inbox, bot,
campaign and fallback UI integration is a subsequent discussion-gated package.

Run `go test -race -p 1 ./pkg/channels ./internal/channelapp ./internal/channelmigration`
with disposable PostgreSQL and Redis URLs in the standard test environment.
