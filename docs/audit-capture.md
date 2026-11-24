# Audit capture foundation

The audit extension is under development. This foundation adds safe diff handling,
an additive provenance schema and a typed internal security-event writer. It does
not yet enable authentication hooks, scoped investigation/export or their UI.
Existing audit read permissions and response shape are unchanged at this stage;
the complete feature must qualify resource/account/case scope before release.

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
capture instead of silently permitting duplicates. Caller authentication and
mutation outcomes are not changed by this library; handler integration and its
failure policy remain a separate implementation/acceptance step.

`ResolveOrigin` defaults to the actual socket peer. Forwarding headers count only
behind an explicitly configured trusted proxy; traversal ends at the first
untrusted hop, from right to left. Malformed/oversized chains fall back to the
peer; an unavailable peer remains unknown. Do not treat every network client as
a trusted proxy. This policy does not change the existing rate limiter or server
configuration. Handler wiring and operator configuration must be qualified before
claiming audit client-IP capture is active.

`ErrorCode` returns bounded operational categories only. Callers must not log raw
database/request/provider errors or pass them as event reasons. No new retention,
tamper-proof guarantee or customer messaging operation is introduced.
