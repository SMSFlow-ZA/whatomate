# Extension MFA and emergency access

`pkg/extensions/security` provides opt-in PostgreSQL-backed application TOTP MFA and separately approved, time-bound emergency access. It does not change shared authentication handlers, issue sessions, register HTTP routes or provide enrollment/login UI. These integrations must be completed and reviewed before claiming MFA enforcement for an application.

## TOTP and enrollment

TOTP uses a random 160-bit secret, HMAC-SHA1, a 30-second time step and six decimal digits, following [RFC 6238](https://www.rfc-editor.org/rfc/rfc6238). Configuration permits only zero or one adjacent time step of drift. Verification accepts a counter only once: a locked credential read and conditional counter UPDATE enforce replay protection across concurrent processes. The enrollment confirmation counter is already consumed and cannot immediately authenticate a login.

Construct `Service` with a host-provided 32-byte AES key and explicit configuration. There is no default or embedded master key. The host must load this key from its secret manager, keep it separate from database backups and make it consistent across application instances. AES-256-GCM encrypts staged and active secrets with a fresh random nonce; associated data binds organization, user and enrollment purpose or credential generation. Wrong-key, identity substitution and modified ciphertext fail verification. Key identifiers, rotation and re-encryption tooling are not provided in this initial version.

`BeginEnrollment` requires `security:mfa.enroll` for the authenticated named user. It stages an encrypted secret until the configured enrollment expiry, which must be positive and no more than one hour. `ConfirmEnrollment` proves possession with a live TOTP, activates the secret and increments credential generation. Replacing an active factor also requires a trusted MFA-verified actor. Old pending enrollment IDs and expired confirmations fail. No administrator bypass or unauthenticated reset endpoint is supplied.

Activation returns ten random 160-bit recovery codes once. Only subject/generation-bound SHA-256 hashes are persisted. `VerifyRecovery` conditionally consumes one hash in the same transaction as its audit event. Concurrent uses have one winner. Recovery codes are authentication factors; they do not establish primary identity or substitute for checking whether the account is active.

Five failed factor attempts cause a durable five-minute lockout. Success resets the failure state. This is per credential, not a complete login/IP rate limiter. The host still needs primary-authentication rate limiting, abuse controls and audited recovery/lockout support procedures.

## Trusted bridge and session generation

The service accepts `extensions.Actor` only from trusted host authentication. The host must never decode actor permissions or its `MFA` boolean from a request body. `VerifyTOTP` and `VerifyRecovery` must run after primary identity verification. A successful call returns a read-only `Verification` with organization, user, verification time and credential generation. A storage/audit/commit failure returns no proof.

The future session issuer must bind this generation into access, refresh and WebSocket session state and compare it to `CheckGeneration` on every protected authentication path. It must also apply its MFA-required and verification-freshness policy. `CheckGeneration` checks revocation generation only; it does not prove a session performed MFA or require that MFA is currently enabled. `Generation` returns a detached status snapshot for that policy.

`Revoke` requires `security:mfa.revoke`, a trusted MFA-verified actor and the expected generation. It atomically increments generation, disables the factor, clears active/pending encrypted secrets, removes recovery hashes and revokes that user's outstanding emergency grants. A previously verified session generation then fails. The generation persists after revocation and must not be reset when re-enrolling.

The current SDK Actor carries an MFA flag but no generation field. Its trusted bridge must validate the session generation before constructing the actor and preserve the credential snapshot throughout sensitive operations. Full primary-auth/session issuance/refresh/WebSocket wiring, race-safe credential snapshot binding and lost-factor administrator recovery are pending. Merely importing this service or setting `Actor.MFA=true` does not provide application MFA enforcement.

## Secrets and API snapshots

`Secret`, `Enrollment` and `Activation` redact secrets in normal formatting, Go formatting and JSON. Enrollment, verification, credential status and grant snapshots have private state and read-only accessors. Returned recovery slices are detached. No API returns encrypted database credentials as a public DTO.

A future trusted enrollment bridge must explicitly call `Secret.Reveal()` to show a provisioning secret or recovery code once over an authenticated encrypted channel. That return value is sensitive and must never enter logs, analytics, incident comments or persisted response caches. Automatic serialization intentionally cannot deliver raw enrollment material. A CLI or UI must deliberately implement this one-time display path.

If activation commits but confirmation is lost, no recovery codes are returned and their plaintext cannot be reconstructed from storage. Reconcile status, authenticate using a fresh TOTP and use an authorized replacement enrollment to generate new recovery codes. A lost successful verification response may have consumed its TOTP counter or recovery code; it never grants a session without proof.

## Separately approved emergency access

Emergency access uses exact named user, organization, resource and permission. The host configures an explicit target-permission allowlist and maximum grant duration. An empty allowlist denies all emergency requests. Requesting a permission outside that list or a duration above the maximum fails. A nonblank reason/reference is mandatory; avoid including protected case narratives in reasons.

- `RequestBreakGlass` requires `security:breakglass.request`, a trusted MFA-verified actor with an active local factor and a named session. The requester becomes the only named beneficiary. The grant expires relative to request time; approval never extends it.
- `ApproveBreakGlass` requires `security:breakglass.approve`, MFA, the same organization and a different named user. Self-approval, expired/revoked grants and stale versions fail. Concurrent approvals have one winner.
- `AuthorizeBreakGlass` requires `security:breakglass.use`, MFA and the exact beneficiary, organization, resource and permission. It rechecks current durable approval, strict expiry, revocation, host allowlist/maximum and the beneficiary's enabled credential generation. A database/audit failure denies access. Infrastructure/organization administration grants no implicit access.
- `RevokeBreakGlass` requires `security:breakglass.revoke`, MFA and an expected grant version. It immediately prevents subsequent authorization. Credential revocation also revokes grants.

Request, approval, use, revocation and identifiable emergency denials are audited inside PostgreSQL transactions. Factor verification/denial, enrollment and credential revocation are also audited without OTPs, recovery codes or secrets. A use cannot return authorization if its audit insert fails. The host must separately provide audit access control, retention/export and operational review.

Emergency access does not automatically bypass the extension Guard or any module's subject-conflict/data-access policy. A module must explicitly opt into an approved emergency action, with resource-specific rules, in its trusted host policy. Authorization returns a decision for the current operation; it must not be cached beyond its expiry/revocation or transformed into a permanent role.

## Storage, verification and pending acceptance

Run `Service.Migrate` explicitly with a migration identity before enabling these APIs. It creates only `extension_security_schema`, `extension_security_mfa`, `extension_security_recovery`, `extension_security_breakglass` and `extension_security_audit`, with a checked v1 schema checksum. Normal runtime identities need restricted DML, not migration rights. The component does not modify core user, session or authentication tables.

Credential row locks, counter CAS, conditional recovery consumption, versioned grants and atomic audit writes protect concurrent changes. Commit errors return `ErrCommitUnknown` with no provisional secret, MFA proof or grant. Unknown request outcomes need durable-state reconciliation; do not assume an absent success response means a transaction rolled back.

Unit tests verify RFC-derived six-digit SHA1 vectors, drift/replay bounds, malformed codes, AES-GCM binding/tamper handling, key length, redaction, reason validation and configuration boundaries. Real PostgreSQL tests use `EXTENSION_DATABASE_URL` and exercise concurrent TOTP/recovery single use, credential-generation invalidation, enrollment expiry, verified-factor replacement/revocation, separate approval, concurrent approval, exact scope/allowlist, expiry boundary, explicit grant revocation and credential revocation. A real audit-failure trigger verifies that recovery consumption rolls back and no proof escapes.

```sh
go test ./pkg/extensions/security
go test -tags integration ./pkg/extensions/security
go test -race -tags integration ./pkg/extensions/security
go vet -tags integration ./pkg/extensions/security
```

CLI/enrollment/login UI, QR provisioning, primary-auth challenge/session binding, API and Guard wiring, key rotation, recovery-code rotation/reset tooling, emergency approval discovery/notification, production access review and full upstream regression acceptance remain pending. No deployment or production MFA claim is authorized by these package tests.
