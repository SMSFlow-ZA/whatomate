# Channels and optional SMS fallback

Channel API **1** is exported from `pkg/channels`. The framework is part of the
application; provider modules have independent versions and are registered at
build time. There is no executable upload or hot-loading endpoint. The SMSFlow
adapter is maintained at https://github.com/SMSFlow-ZA/whatomate-smsflow.

This feature is undergoing acceptance. Publishing source does not establish
provider availability or authorize real SMS. The default WhatsApp API and
unmanaged organization behavior remain available.

## Connect and send

Administrators use **Settings → Channels**. `settings.channels:read` permits
inspection, and `settings.channels:write` permits creation, credential rotation,
configuration and stopping a connection. Existing Admin roles receive these
permissions. Connections are scoped to the current organization. Credentials
are encrypted using the deployment encryption key and never returned to clients.

SMSFlow customers use their own dedicated SMPP-enabled Client ID and Client Secret. SMS fees
are charged by SMSFlow, independently of the Whatomate subscription. Validation
sends no SMS. Connection-prefixed SMPP message references filter report routing
even if credentials are reused in another workspace. A failed validation retains a disabled
connection for revalidation with the same identity.

The adapter reuses the existing SMPP integration's HTTPS contract for OAuth,
registration, submission, statuses and replies. It needs no new SMSFlow API or
per-instance container. `WHATOMATE_CHANNELS__SMSFLOW_REPLY_ROUTES` is a trusted,
comma-separated deployment list of verified two-way routes; it defaults empty.
One-way sending is available without advertising reply-bot capability.

The inbox, campaign editor, bot message actions and API can select SMS. Normal
agents can use enabled connections with their existing sending permissions;
they cannot retrieve credentials. Current account/role/contact authorization,
suppression, consent, destination and segment limits are rechecked before SMS
dispatch. SMS requires an international telephone number; a WhatsApp-only
business-scoped identifier is insufficient. The default limit is three segments,
editable from one to ten. The preview counts GSM extension characters and UTF-16
code units; messages are never silently truncated.

One-way accounts can send. Replies and automatic reply bots require a verified
two-way route. SMS supports submitted, delivered, failed and unknown states;
it has no remote read receipt. Reading a conversation internally does not create
a carrier read receipt or reopen WhatsApp's 24-hour session window.

## Fallback rules

Fallback starts **off**. Each inbox send, campaign, bot action or API request
opts in explicitly with a connection and recipient consent. A confirmed
recipient-undeliverable WhatsApp failure (currently Meta code 131026) can trigger
SMS. Authentication, configuration, policy, suppression, rate-limit and uncertain
submission errors do not trigger it. New eligible codes require a reviewed
classification.

An optional undelivered timeout defaults to ten minutes, configurable from one
minute to 24 hours. It requires confirmed WhatsApp submission and does not mean
"unread." Confirmed delivered/read cancels a pending fallback. The worker checks
delivery again when claiming it. At most one automatic SMS attempt exists for a
logical message, with separate WhatsApp and SMS history. Both messages can arrive
if WhatsApp delivers after the SMS has been submitted; an in-flight send cannot
be recalled.

Plain text can be reused. Templates, media and interactive content require an
authored SMS alternative. Bot button alternatives must explain a usable text
response; SMS does not carry WhatsApp buttons. Multiple CTA cards in one bot
node share one SMS alternative on the first card.

## API examples

Existing requests without channel fields retain WhatsApp behavior. Authenticate
as usual, with the current organization and CSRF headers for browser sessions.

`POST /api/contacts/{contact_id}/messages`:

```json
{
  "type": "text",
  "content": { "body": "Your order is ready" },
  "channel": "sms",
  "client_message_id": "<caller-generated UUID>",
  "channel_connection_id": "<connection UUID>",
  "consent_confirmed": true
}
```

Generate `client_message_id` once per intended SMS and reuse it when retrying
an uncertain HTTP request. Repeating the same UUID and payload returns the same
logical message; conflicting content is rejected. Omitting it creates a new
message on each request. This host-side safeguard does not make repeated calls
to the underlying SMPP submission endpoint safe.

For WhatsApp with fallback, omit `channel` and add:

```json
{
  "sms_fallback": {
    "enabled": true,
    "connection_id": "<connection UUID>",
    "text": "Your order is ready. Reply to this SMS for assistance.",
    "consent_confirmed": true,
    "timeout_enabled": true,
    "timeout_minutes": 10
  }
}
```

The same fallback object is accepted by template sends and media multipart
requests (as a JSON `sms_fallback` form value). Direct SMS is text only.
`POST /api/channels/preview` accepts `text` and `max_segments`. Connection
management uses `/api/channel-connections`; installed providers and aggregate
health are at `/api/channels/providers` and `/api/channels/health`.

SMS campaigns use `channel: "sms"`, `channel_connection_id`, `sms_body` and
`consent_confirmed`; no dummy WhatsApp account/template is required. Recipient
`template_params` fill authored `{{name}}` variables in SMS or fallback text.
Final rendered content is checked against the segment limit for every recipient.

## Bots

An SMS flow binds explicitly to an inbound-capable connection with automated
reply consent. SMS sessions and inbound checkpoints are separate from WhatsApp.
Supported SMS graph nodes are start, text message, prompt, authored text choices,
condition, timing, set-variable and end. The editor hides unsupported nodes and
the server rejects them. External API/webhook/AI side effects, transfers,
cross-flow jumps and WhatsApp forms are not executed inside an SMS transaction.

WhatsApp flows retain their existing nodes. Text message/prompt/end actions can
select direct SMS or fallback. Button actions support WhatsApp with an authored
SMS fallback. Stable inbound/flow/node identifiers prevent re-executed actions
from queuing another SMS. A conflicting payload for the same action is rejected.

## Durable processing and recovery

Messages, attempts, events and bot jobs live in the existing PostgreSQL database.
The existing Redis campaign queue remains in use; there is no new sidecar.
Provider requests run outside database transactions. Atomic claims and two-minute
leases protect outgoing attempts. An expired lease or lost POST response becomes
unknown, and is reconciled by its original identifier instead of being resent.

Polling runs every 30 seconds with bounded exponential backoff. Events and their
connection-scoped checkpoint commit together. Duplicate events are ignored;
unknown correlations are quarantined. Bot input, session transition and outgoing
queues commit together. Provider event polling and outgoing dispatch run in
separate bounded worker lanes, so a slow send does not block receipt processing.

Connection **Stop pending SMS dispatch** cancels unclaimed work and pending bots,
while preserving submitted/unknown deliveries for reconciliation. The trusted
deployment switch `WHATOMATE_CHANNELS__STOP_DISPATCH=true` stops all new SMS and
SMS bot dispatch. It does not recall carrier-accepted messages. Event retrieval
and reconciliation remain available while stopped.

Back up the database plus the existing encrypted runtime configuration/key.
Connections, cursors, attempts, receipts and bot jobs are required recovery
state. Restore with dispatch stopped and reconcile downstream submissions before
re-enabling. A database rollback cannot prove that a carrier did not accept an
SMS. Never reset an unknown attempt to queued to "repair" it.

Health includes provider/poll failures, balance validation failures, low balances,
unresolved submissions, blocked/stale bot work and fallback volume. Balance units
are provider-reported credits/currency, never an invented conversion. Credentials
and message bodies do not belong in operational alerts.

## Provider compatibility

Implement `channels.Provider` and advertise only supported capabilities.
Validation must not send a message. A text connection requires a recovery path
through direct reconciliation or delivery reports; inbound is optional. Stable
event identifiers and connection-scoped opaque cursors are mandatory for polling.
Return safe classified `ProviderError` values, never credential-bearing errors.
Register the implementation in the host composition root, pin its module version
and rebuild/publish corresponding source with the application image.

The host commits a durable one-shot claim before Send. It never calls Send again
for an unknown or expired claim. Providers must truthfully advertise transport
idempotency and must not retry a submission automatically without a durable replay
guarantee. The existing SMPP adapter has no direct lookup: Reconcile retains the
unknown status and Poll can resolve late delivery reports. Missing receipts never
prove the original SMS was unsent. A new logical message is a new explicit send.

The superseded V2-only preview is not compatible with this adapter's cursors.
Retain its data for review and revalidate/replace test connections with dispatch
held. Never reset existing unknown work to queued. No standard-V2 API migration
is needed by this implementation.
