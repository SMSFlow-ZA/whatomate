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
bootstrap or external provisioning writes. Scoped investigation/export remains
unfinished. The feature is not yet ready for a managed release. Existing dispatch
holds and automation defaults are unchanged.

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
