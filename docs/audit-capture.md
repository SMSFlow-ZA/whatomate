# Audit capture foundation

The audit extension is under development. Current source includes safe diff
handling, additive provenance, authentication observations, qualified request
origins and atomic role/user/membership events. Integration and initial-identity
provisioning hooks, scoped
investigation/export and their UI remain incomplete. Existing audit read
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

Administrative user creation/restoration, requested role or authority changes, credential/SSO/
active-state changes and membership changes also commit with their security
records. User/membership writes roll back together if their transactional capture
fails. A no-op membership role update does not invent an authority transition.
Deletion retains verified ownership evidence; membership removal affects the
selected organisation, while a native user deletion invalidates that identity.
An archived identity cannot be claimed from another organisation by reusing its
email. Restoration compares the deleted state in the write, so a second request
cannot overwrite credentials from a completed restoration. Native deletion also
checks the user's organisation at the write boundary.

Redis session fences cannot share a PostgreSQL transaction. The successful
pre-update fence is a separate `auth.sessions_revoked` observation with reason
`before_security_update`; it remains true even if the later database change rolls
back. The committed credential/security change is `user.security_changed`, recorded
in its database transaction. If the final Redis fence fails, the HTTP request can
return 503 while the database security stamp and its successful change record
remain committed. Outcomes describe the recorded operation, not a promise that
every later step in the HTTP request succeeded. Old stamped sessions remain
invalid after the committed database change.

Reserved support-user mutation denials use a fixed protected-resource reason;
they do not include the requested target's email or raw URL. Malformed user-update
bodies are diagnosed with a fixed code, never by logging password-bearing input.
Attribution refuses stale or unavailable identity rather than inventing a verified
actor; genuine identity-storage failures emit a bounded safe diagnostic.

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

## Integration configuration capture

WhatsApp account, outbound webhook, custom-action and SSO-provider creation,
editing and deletion commit with typed `integration.changed` records. Workflow
integration definitions and the organisation's integration switch use the same
fixed metadata contract. Credential replacements have a fixed rotation reason;
neither credential values, custom-action configuration nor mapped workflow input
is copied into the typed event. Account records carry a verified account identity;
other integration resources do not invent a conversation or account relationship.
Sensitive configuration writes suppress SQL payload logging even when database
debugging is enabled; operational diagnostics retain fixed error categories.

Existing-target updates lock and verify organisation ownership. The account,
webhook, custom-action and SSO-provider editors also compare their loaded version
before writing, so a concurrent configuration change requires a reload. Account
default switches and their capture share the same transaction. Verified deletion
evidence is recorded before a hard deletion, in that deletion's transaction.

Channel creation has two durable phases. First, an encrypted disabled identity and
its creation event commit. Provider validation runs outside database locks and
does not send a message. Successful activation, capabilities, balance metadata and
`integration.validated` capture then commit together after a version check. A
failed or lost validation leaves the created disabled identity available for
review; it is not labelled as a successful validation. Concurrent changes/removal
cannot be overwritten by the validation result. Updating/revalidating a channel
records successful validation only when the corresponding configuration commits.
Audit failure rolls back that configuration and any pending-work cancellation;
uncertain submissions keep their existing reconciliation state.

These records cover the named application handlers, not arbitrary direct database,
bootstrap or external provisioning writes. Scoped investigation/export APIs are
under local qualification; the updated audit interface and managed release remain
unfinished. Existing dispatch holds and automation defaults are unchanged.

## Registration and onboarding provenance

Native registration and policy-enabled SSO auto-creation commit the identity,
membership and `identity.provisioned` observation together. This event has no
authenticated human actor: it displays `Provisioning policy`, with
`actor_verified=false`. It does not claim that an administrator created the user.
The selected role belongs to the target organisation and is checked inside the
transaction. Existing password-authenticated cross-organisation joins compare
the current credential/security snapshot before granting membership; they cannot
revive a removed membership. Existing native organisation and role data remain
unchanged. SSO provisioning rechecks the exact saved provider/policy after its
network calls; a changed or disabled policy cannot grant access using the earlier
OAuth snapshot.

Successful registration sign-in is a separate best-effort authentication event,
recorded only after both session tokens exist. If session creation fails after
provisioning commits, the provisioned identity/membership remain and the sign-in
is recorded as failed. No successful authentication is inferred from account
creation alone. Authentication tokens and password hashes never enter these rows.

Embedded WhatsApp signup commits an encrypted pending account and its creation
event before remote registration/subscription calls, on managed and unmanaged
instances. Account reservation serialises competing creations for the same
organisation/phone identity. An archived identity requires administrator review;
signup cannot resurrect it. Remote calls run outside configuration locks.
Afterward, a version-checked transaction saves credentials/status and the matching
`integration.registration_updated` observation. Manual registration uses the same
scoped version fence and atomic local capture. Concurrent edits/removal are never
overwritten. The registration reason describes the saved active, pending or
inactive local state; it is not independent proof of Meta registration success.

Meta operations cannot participate in a PostgreSQL transaction. If Meta may have
processed registration but local saving/capture fails, the API explicitly asks
the administrator to check account/Meta status before retrying. No automatic
remote replay is added. Provider error bodies, authorization codes, credentials
and PINs are absent from audit/diagnostic payloads. Existing write-authorised
setup responses may still show the webhook verification token and generated PIN
needed for configuration; this does not grant audit readers access to them.

## Investigation and paginated export

The existing `GET /api/audit-logs` and `GET /api/audit-logs/{id}` now load the
viewer's current user, selected-organisation membership, role and permissions
from PostgreSQL. Cached role/superadmin flags cannot grant audit access. A removed,
nil or foreign membership role cannot inherit the native role. Supported older
native identities without a membership row retain their native role behavior.
Stamped sessions must still match the current identity.

`audit_logs:read` is required together with the underlying resource's current
read access. Workflow-integration configuration follows its existing general
settings write boundary; escalation settings also require full inbox access.
The same scope applies before filters, count, paging, details, choices and export.
Contact-only history cannot borrow an assigned case on another account. Explicit
case context must satisfy the existing current case-history access rules. Former
assignment is insufficient. Conversation activity and workflow run history retain
their own scoped APIs; security investigation never infers conversation context
from a contact or fabricates events from message history.

Known soft-deleted configuration targets retain verifiable historical ownership.
Verified typed ownership can retain a hard-deleted target's history, without a
link to a current resource. Unknown legacy ownership is unavailable, including to
administrators; a random ID/name is not proof. Staff identity and origin details
are masked without directory access, apart from the viewer's own recorded actor
label. Policy provisioning uses its fixed non-human label. Current resource/actor
availability flags do not grant access to another API.

Responses keep existing fields and add captured/legacy/unsupported context,
masking and availability flags, capabilities, `as_of`, `has_more` and
`next_cursor`. Zero-version rows never acquire an inferred IP, request ID, outcome
or verified actor. Sensitive diffs are sanitized on every read. Oversized or
invalid-shape historical diffs are replaced in the query result with an
unavailable marker before decoding; stored history is unchanged.

Supported filters are `resource_type`, `resource_id`, `user_id`, `action`, `from`,
`to`, `category`, `event`, `outcome`, `request_id`, `account_id` and `case_id`.
Malformed/unknown/repeated parameters fail rather than silently widening a query.
Date-only bounds mean UTC days; the upper day includes every subsecond instant
before the next UTC midnight. RFC3339 bounds include the exact supplied timestamp.
Use `outcome=unavailable` for context that was not captured. Actor ID zero selects
system/unverified observations, not a user. Phone/contact permissions remain in
force regardless of these filters.

Lists retain valid page/limit requests (50 by default, 100 maximum). Cursor paging
uses descending creation time/record ID, a fixed first-page time boundary and the
same viewer/filter context. A cursor is only a position, never an access grant.
Changing filters or viewer requires restarting paging; every request rechecks
current access. `GET /api/audit-logs/filters` returns scoped choices, not a staff
directory. Actor choices are explicitly limited to 100, with a limit indicator.
Storage failures produce a recoverable error and a server-owned support reference,
not an empty successful result or a guessed not-found response.

`POST /api/audit-logs/export` accepts the same filters with an optional cursor and
a limit of 1–1000 (default 1000). It additionally requires `audit_logs:export`.
One CSV page is bounded to 4 MiB; reduce the limit/range if it is too large.
`X-Audit-Has-More` and `X-Audit-Next-Cursor` explicitly identify additional pages;
no silently truncated complete-export claim is made. CSV contains readable,
sanitized data only, uses UTF-8/CRLF, and escapes spreadsheet formula prefixes
after whitespace, controls or a byte-order mark. Download responses are not cached.

After generation, authority and the returned record IDs are checked again. A
permission/assignment change stops the export. An `audit.exported` observation
with reason `export_prepared` must commit before bytes are returned; failure
returns no CSV. This records server-side generation, not receipt by the client.
No filters, diff values, credentials or CSV body enter that observation.

The new export permission is granted to default new Admin roles. When it is first
introduced on upgrade, only existing system Admin roles that already have audit
read access receive it. Creation/grant is transactional. Later explicit removals
survive startup, and custom roles/managers are not silently promoted. Additive
organisation/time/ID and organisation/actor/time/ID indexes support investigation.
No new retention period, entitlement or tamper-proof guarantee is introduced.
