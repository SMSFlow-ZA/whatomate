# Compiled extension boundary (development)

`pkg/extensions/v1` is the generic public SDK. It exports descriptors, explicit
organization actors/permissions, immutable protection values, dependency/API
validation, scoped storage/audit/job/messaging contracts, worker/migration and
flow-node types. Registry snapshots are detached; namespace escapes and wildcard
permissions are rejected. Policy absence, errors and panics deny access without
exposing policy payloads. No internal models or database handles are exported.

`pkg/extensions/postgres` implements organization/module-scoped record CAS,
atomic audit/outbox writes and explicit checksum-tracked storage migration.
Duplicate inserts conflict. Commit errors are Unknown. Job claims use locked
rows and random lease tokens; expired leases are held for reconciliation rather
than retried. Restores must call the hold operation before worker startup.
Dispatch must still check current eligibility, suppression and case access.

The host calls `InitializeExtensions` before channel workers start. Trusted
`extensions.required` and `extensions.required_inbound` are comma-separated
module IDs. Missing compiled modules refuse startup. An inbound guard runs before
contact creation, reaction processing, media fetch, persistence, callbacks or bot
processing. No sender/profile is logged before that guard. Unconfigured hosts
retain ordinary behavior. These hooks are not a protected transcript system.

The dispatcher also gates special message branches before logging/persistence.
When inbound policy is required, unsupported non-message ingress (calls and
coexistence sync/echo events) is rejected rather than bypassing that contract.
Template status events and ordinary message delivery statuses remain supported.
Campaign worker construction also refuses required/protected extension settings
until that process has an authenticated composition and dispatch policy. Running
the standalone worker command cannot bypass the server readiness gate.

`extensions.protected_mode=true` currently refuses startup deliberately. The
SDK protection value is not yet persisted on messages or applied to every core
API/search/preview/export/webhook/WebSocket/notification path. Application MFA
and extension domain endpoints also need the authenticated composition bridge.
Never store real protected records in the generic host transcript in this state.
No permissive configuration flag bypasses the readiness gate.

The build-time Vue registry is integrated with router/navigation and checks
explicit extension role grants; see [frontend contract](extensions-frontend.md).
Its catalog is empty. No runtime script/plugin URL is loaded. Add-on source lives
in its own AGPL repository; customer content/configuration belongs elsewhere.

Before activation: implement protected persistence and all access/event paths,
compose real modules, enforce current authenticated grants/MFA, finish domain UI,
dispatch/recovery and migrations, verify omission/disable/rollback boundaries,
and run end-to-end authorization, concurrency and regression acceptance. This
development code is disabled by default and has not been deployed.

## Optional durable knowledge vectors

`extensions.knowledge_vector_enabled` defaults to false. Enabling it requires the
compiled knowledge/AI modules, an actual administrator-installed `public.vector`
extension, dimensions 1–2000, and `knowledge_vector_identity` identifying reviewed
exact embedding deployment/model-generation evidence. A moving logical
`ai_embedding_model` alias is not that identity. Release composition must establish
the external model identity; a nonempty configuration value does not verify it.

The host exposes `initializeExtensionVectors(ctx, db)` for the startup migration
phase after module/provider initialization and before protected activation. It
enumerates trusted current host organizations, creates immutable tenant vector
profiles and registers the fixed internal indexing principal. A disabled indexing
grant remains disabled after startup. There is no browser registration/indexing
route and no application `CREATE EXTENSION`.

`extensionVectors(ctx, db, tenant)` performs verification only, never DDL or grant
registration. It rejects a different host database pool, unknown/deleted tenant,
missing vector relation/profile, changed model-generation identity or dimensions.
The operator AI ServiceFor path uses this helper after authenticated actor refresh
and knowledge grant synchronization; native vector failures deny service creation.
When disabled it leaves existing FTS/candidate behavior intact. Assign a returned
repository to `ai.HybridRetriever.Vectors` only when its pointer is nonnil, to avoid
a typed nil interface selecting the native path accidentally.

`extensionVectorTick(ctx) error` is the bounded indexing hook for an independently
scheduled server worker lane. It creates the same configured provider, verifies
each current organization/profile without migration and processes at most one page
per organization. Remaining deadline is shared across organizations; failures are
joined after other tenants are checked. The provider executes outside database
transactions, and the knowledge owner revalidates publication/page/grant state
before committing the derived vector. Schedule it separately from ingress,
delivery settlement and escalation; do not invoke it from request handling.

The native backend uses exact pgvector cosine search and keeps FTS first. It does
not establish large-workload capacity or install an approximate vector index.
Publication/page erasure cascades to derived vectors. See the add-on repository's
`docs/knowledge-vector.md` for source currentness, worker and model-generation
boundaries. Host hook tests use isolated schemas on actual PostgreSQL/pgvector,
not a plain PostgreSQL substitute. These hooks do not by themselves establish
complete protected-host/deployment acceptance.
