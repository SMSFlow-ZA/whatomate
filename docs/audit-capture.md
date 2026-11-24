# Audit capture foundation

The audit extension is under development. Current source includes safe diff
handling, additive provenance, authentication observations, qualified request
origins and atomic role-change events. Session-revocation/integration hooks,
scoped investigation/export and their UI remain incomplete. Existing audit read
permissions and response shape are unchanged at this stage; the complete feature
must qualify resource/account/case scope before release.

## Diffs and historical data

New configuration audit writes and existing list/detail reads redact credential-
shaped fields recursively. Only field/old/new data is retained in the response;
extra payload keys are dropped. Depth, nodes, strings and changes are bounded.
Redaction copies data; reading a historical diff does not rewrite stored history.
This is field-based protection, not a claim to identify secrets disguised as
arbitrary ordinary text. New security events have no request/body/changes field.

Rows with context version0 have unavailable provenance. Migration does not infer
an IP, outcome, verified actor or request identifier from later data. Legacy SQL
writes retain version0; the new columns/index do not require older clients to
supply new fields. This is additive schema compatibility, not proof of a complete
previous-image or backup-restoration rehearsal.

## Typed security events

`internal/audit.RecordSecurity` accepts fixed event/outcome/reason codes, verified
identifiers and server-owned request context. It loads the actor's label itself,
validates organisation/actor/target and optional case/account agreement, and writes
no message content, credential, session identifier or arbitrary error payload.
Failed credential checks can identify a known target without labelling the actor
as verified. Unknown-account attempts cannot be assigned to a guessed tenant.

Capture is synchronous, bounded to two seconds (or the caller's shorter context),
and participates in a supplied database transaction. Repeated identical capture
for one request/event/resource uses a partial unique constraint. Changed evidence
under that key rejects rather than altering history. A missing constraint fails
capture instead of silently permitting duplicates. A cancelled blocked database
statement retains its deadline/cancellation category even if the driver returns
an opaque connection error.

## Authentication and privileged changes

Password login records a successful session only after both tokens are created.
Known-target wrong-password or disabled-account outcomes have an unverified actor.
An unknown address creates no tenant-attributed event. Session-creation failures
are recorded as failures, without tokens or credentials. Successful Microsoft SSO
is distinguished from other supported SSO providers; denied identity links are
captured only for a known target in the provider's organisation. Foreign identities
and invalid OAuth state cannot be assigned to an inferred tenant.

Logout observations require a valid signed refresh token. Session removal,
already-removed sessions and storage failures have distinct fixed reasons. Missing
or invalid tokens do not invent an actor. The current writer requires an active
actor; a disabled/deleted actor cannot create a verified sign-out row, and failed
capture is reported through a safe diagnostic. This is not a complete sign-out
history for expired or unverifiable sessions.

Authentication and permission-denial observations are bounded, synchronous and
best effort. Audit-storage failure leaves the existing HTTP result intact and
emits a fixed error category. Known privileged permission denials carry a static
permission key and organisation identity, not the requested URL, body or guessed
resource ID. Stale session identity is rejected before attribution.

Role creation, editing (including system-role permission edits) and soft deletion
commit with their security event in the same transaction. If capture fails, the
role's fields, permission associations and default changes roll back. Configuration
diffs remain separate. No existing role-edit permission or authoring semantics are
expanded by this change.

`ResolveOrigin` defaults to the actual socket peer. Forwarding headers count only
behind an explicitly configured trusted proxy; traversal ends at the first
untrusted hop, from right to left. Malformed/oversized chains fall back to the
peer; an unavailable peer remains unknown. A server-generated request UUID is
shared by observations in one request; incoming request/correlation headers cannot
choose it. Each HTTP request, including requests on a reused connection, gets a
fresh identity.

Operators can set comma-separated exact proxy CIDRs with
`audit.trusted_proxy_cidrs` or `WHATOMATE_AUDIT__TRUSTED_PROXY_CIDRS`. Default is no
proxy trust, independently of the rate limiter's proxy boolean. Startup rejects
malformed, universal or oversized trust policies. Explicitly configure only
verified proxy networks; an IP labelled `peer` identifies the socket peer, not an
inferred end user. This does not change rate-limiter behaviour.

`ErrorCode` returns bounded operational categories only. Callers must not log raw
database/request/provider errors or pass them as event reasons. No new retention,
tamper-proof guarantee or customer messaging operation is introduced.
