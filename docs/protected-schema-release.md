# Offline schema installation and DML-only protected runtime

Ordinary hosts retain their existing startup migration behavior. A protected
deployment may opt into `database.schema_managed_externally=true`. This mode
requires a reviewed, compiled release identity and source-derived expected SQL
catalog. Configuration alone cannot enable a migration bypass.

The runtime role must be a dedicated LOGIN with no superuser, CREATEDB,
CREATEROLE, replication, BYPASSRLS, role memberships, database/schema ownership,
database CREATE/TEMP, schema CREATE, table/routine ownership, table TRIGGER,
REFERENCES/TRUNCATE, or privileged server-file function execution. Revoke PUBLIC
TEMP/CREATE on the dedicated database and schema. Grant business SELECT/DML and
necessary business sequence access explicitly. Keep bootstrap/schema owner
credentials in offline operator custody; never mount them into the application.

The runtime has SELECT-only access to schema seals/attempts, protection state,
bootstrap attempt, migration source/version metadata, vector profiles/indexer
grants, and `er_processing_authorization_bindings`. Never grant application or
processing workers DML on these policy tables, including column-level grants.
The SQL role checker rejects these privileges rather than relying on deployment
claims. This does not certify that an arbitrary business DML grant is necessary;
the deployment still owns its explicit business-table allowlist.

## Build and reference catalog

Compile `cmd/extensions-schema` with
`-X github.com/shridarpatil/whatomate/internal/protectedschema.ReleaseIdentity=HOST_SHA:ADDONS_SHA`
(optionally append `:PROFILE_SHA256`). Both source commits must be exact lowercase
40-character hashes. The optional profile identifies a separately reviewed
schema/config generation. The source tree and private release process must bind
these values to the actual corresponding source; the binary cannot independently
prove its own build provenance.

Create a **new disposable database**, with the exact target PostgreSQL major,
installed extension versions, and vector profile configuration. For example, a
deployment using PostgreSQL 17/vector 0.8.7 must generate its reference there,
not in a different test server. Install `vector` administratively before the
tool when enabled; the app never creates extensions. Use a root-owned 0600 TOML
file with a schema-owner DB connection, `ssl_mode='verify-full'`,
`schema_managed_externally=true`, exact `schema_release`, protected mode,
synthetic development disabled, generic plan disabled and carrier dispatch
disabled. Provide the reviewed TLS CA through the operator environment. The CLI
loads only its file; ordinary `WHATOMATE_*` config overrides are not applied.

Run `extensions-schema -baseline -config /private/reference.toml
-catalog-output /private/NEW-reference-catalog.json`. The absolute output and
its ancestor directories must be root-owned, non-writable by group/others and
contain no symlinks. Reference mode refuses a nonempty user schema, executes
source migrations only, and produces no runtime seal. It creates a synthetic
reference admin with a random password; reference data is not deployable data.
No listener, Redis, channel, model provider or background worker is constructed.

Review the canonical manifest and compile the runtime **and** final owner tool
with the same ReleaseIdentity plus
`-X github.com/shridarpatil/whatomate/internal/protectedschema.ExpectedCatalogSHA256=REVIEWED_SHA256`.
Also set the runtime's existing `main.SourceRevision` to the matching HOST_SHA.
Configure exact `database.schema_release` and `database.schema_sha256`; empty or
mismatched compiled/configured values refuse startup. Preserve the reviewed
manifest and matching corresponding-source material with the release artifacts.

The manifest includes relation/column/default/type definitions, constraints,
index validity, trigger definitions/enabled state, routine bodies/settings,
policies, sequence definitions, source migration checksums/versions, PostgreSQL
major and installed extension versions. It excludes business row contents,
owner role names, ACLs, sequence counters and per-instance processing bindings.
Role/ACL posture is checked separately. Runtime data never enters the manifest.

## Explicit protected-database upgrade and seal

Stop/isolate the protected runtime and all processing/transport workers. Keep
dispatch and restore holds in effect. This tool requires an existing protected
database with exactly one organization and its specified existing default admin;
it is not an organization or worker onboarding mechanism. Run the final compiled
owner tool with `-install -organization-id EXACT_UUID -config /private/owner.toml
-catalog-output /private/NEW-installed-catalog.json`. It holds one physical
exclusive advisory session across all owner APIs, writes a durable attempt
before migration, then installs source schemas/guards and compares the result
with the **compiled** catalog. It appends the immutable release seal last.

Core GORM migrations run two explicit successful passes because forward
Message/DeliveryAttempts associations materialize their foreign key only after
both tables exist. Errors are not retried. Vector registration occurs only in
this offline owner phase and never re-enables a revoked indexer grant. Existing
bootstrap-attempt storage is included in both reference and target shape.

Only the latest monotonically numbered seal authorizes a runtime. A historical
release cannot restart after a newer release even if their SQL catalogs match.
The tool refuses repeated release/hash attempts. A deliberate reviewed rollback
needs a new explicit release/profile installation; no automatic old-image
fallback is supported. Only protection-capable compatible artifacts may ever
open a protected database.

Any failed/unknown owner operation leaves the database held. A durable attempt
without a seal blocks both runtimes and subsequent installers, even after loss
of the owner's original connection. The tool cancels pool-owned migration work
on exclusive-session loss and rechecks the same physical lock before sealing.
Do not delete attempts, rerun, automatically reseal a target capture, erase the
database or claim a partial operation succeeded. Inspect privately and use a
separately reviewed recovery/release procedure. An output file reserved but
empty is likewise an unresolved operation, not permission to retry.

## Runtime and processing boundaries

The protected server uses `protectedschema.OpenRuntime`; external-mode module
initialization rejects an arbitrary unguarded SQL pool. Every physical pool
connection acquires a shared advisory migration fence and verifies the latest
compiled seal, pending-attempt absence and protected marker before handout.
Startup additionally verifies the full catalog and role posture in a read-only
transaction. The original lease is monitored and its loss stops the process.
Surviving/reconnecting pool connections also retain/refuse the migration fence.
The owner installer cannot start while any such connection remains. The generic
standalone campaign worker and server `-migrate` are refused before DB work.
Media publication recovery and module registration issue verification SELECTs
instead of startup DDL. Protected external mode does not support generic managed
plan installation.

This fence coordinates reviewed tools and ordinary application SQL. It does not
prevent a privileged offline owner from ignoring the protocol, nor deliberate
arbitrary SQL credential tampering such as stripping pool fences with
`pg_advisory_unlock_all()` or `DISCARD ALL`. Runtime source does not issue those
calls; normal shutdown releases its dedicated lease. The fingerprint is verified at
startup; it is not a continuous superuser-tampering detector.

Processing authorization now has a fixed source-defined function body and
`er_processing_authorization_bindings` table. Empty bindings deny every worker.
The processing CLI emits private owner SQL to insert exact database/session
role/org/user/profile/audience bindings and grants the NOLOGIN function owner
SELECT-only access to them. The application and processing worker cannot edit
bindings. Existing function body hash, NOLOGIN owner, ACL/current-grant checks
remain mandatory; binding-table/column write regrants also refuse processing.
Replacing an older interpolated function requires an explicit offline owner
upgrade and a new reviewed function hash. Creating this schema or sealing a
release does not create worker identities, grants, MFA, source approvals,
external deployment attestations or actual model/carrier readiness.
