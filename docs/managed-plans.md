# Managed plan enforcement

This AGPL fork adds host-controlled allowances to upstream
`v0.1.1-beta-0.17`. Public billing and customer administration cannot change the
allowances. Existing unmanaged deployments retain upstream behavior.

Every managed instance supports exactly one non-deleted organization, regardless
of its subscription plan or administrator role. Database guards enforce this
boundary for creation and restoration, including concurrent and direct writes.
The UI hides organization creation and reports organization usage. Existing
organizations found during an upgrade are preserved; additional organizations
cannot be created or restored while one is present.

Limits apply across every organization in an isolated instance. All non-deleted
customer users count, including its owner and inactive users. A single verified,
active support superadmin is excluded by immutable user ID. Its identity, access
and organization memberships are protected from customer modifications. Every
non-deleted WhatsApp account counts, including pending and inactive accounts.
PostgreSQL guards serialize additions and restores, including direct application
database writes, so simultaneous requests cannot exceed the last available slot.

Media allowance counts regular files under both configured uploads and audio
directories. Existing files are included. Every persistent application writer,
including incoming WhatsApp media and generated speech, uses a database lock,
an authoritative filesystem scan and an atomic file replacement. Storage failures
block writes. Symlinks and writes outside these roots are rejected. Temporary
conversion files have separate bounded sizes and timeouts; the deployment must
also provide a bounded temporary filesystem. Database, Redis, configuration and
encrypted backups are operational persistence outside the customer media allowance.

A lower allowance preserves existing data and ordinary updates. New additions
are blocked until usage fits; reductions and equal-sized media replacement remain
possible. API quota failures use HTTP 409, code `plan_limit_reached`, and resource
`organizations`, `users`, `numbers` or `storage`. Reserved support changes use HTTP 403 and
`support_protected`. Authenticated `GET /api/plan` reports aggregate usage and
allowances. There is no HTTP endpoint for changing limits or exposing bootstrap
credentials. The UI displays usage and provides a refresh action.

Managed startup requires local media storage, disabled call recording, PostgreSQL,
valid positive allowances and corresponding source matching the binary's compiled
revision. It creates a missing support account only once using a host secret;
existing support passwords are never reset. Start the server with migrations and
the configured initial owner before standalone workers. Once guards are installed,
starting this fork without managed configuration fails closed.

Example trusted environment (use a secret store for the password):

```text
WHATOMATE_PLAN__ENABLED=true
WHATOMATE_PLAN__MAX_USERS=3
WHATOMATE_PLAN__MAX_NUMBERS=1
WHATOMATE_PLAN__MAX_STORAGE_BYTES=5368709120
WHATOMATE_PLAN__SUPPORT_EMAIL=support@example.com
WHATOMATE_PLAN__SUPPORT_PASSWORD=<host-managed-secret>
WHATOMATE_PLAN__SOURCE_URL=https://github.com/SMSFlow-ZA/whatomate/commit/<40-character-revision>
```

`GET /source` is public and redirects to the exact corresponding-source commit;
the source link is visible on the login page and application pages. Build from
that published commit:

```sh
docker build -f docker/Dockerfile.managed-musl --build-arg VCS_REF="$(git rev-parse HEAD)" -t whatomate-managed .
```

The managed inbox image uses `docker/Dockerfile.managed-musl`: a digest-pinned
Alpine runtime, a pinned Go security release, native musl builds of Piper and
verified FFmpeg sources, and the retained voice-model data. The Debian recipes
remain available for their separately verified runtime variants. The final image
runs as user/group `10001:10001`. Production deployments
should pin the derived image digest and the same public source revision. See
[dependency and runtime security](dependency-security.md) for scan scope and
remaining distribution risk. Before deployment run
`go test -p 1 ./...` with disposable PostgreSQL and Redis, plus frontend type
checks, unit tests and production build. Upstream packages share a test database
and must run serially; quota tests themselves exercise concurrent requests.


## Inbox release and recovery checks

Inbox workflow remains organization opt-in and is available independently of the
hosted plan. New rules are disabled. Keep `inbox_automation.dispatch_enabled=false`
and `channels.stop_dispatch=true` in disposable and recovery configurations. These
are independent controls; enabling an inbox workflow does not authorize sending.

Before promoting a managed inbox image:

1. Pass the PostgreSQL-backed serial Go race suite, Vue type/unit checks and the
   synthetic Chromium inbox workflow suite. The managed image workflow requires
   the reusable `inbox-quality.yml` acceptance job before building and scanning.
2. Exercise existing and new cases, multiple accounts with the same contact,
   restricted agents and supervisors. Check immutable policy assignments,
   completed reply cycles, scoped queue counts, field filters, capacity and
   confirmed macro actions. Keep test providers and recipients synthetic.
3. Restore a full database and file backup on an isolated instance. Include
   `inbox_sla_versions`, `inbox_sla_obligations`, `inbox_capacity_presets`, case
   policy references, saved views, workflow metadata and run outcomes. Older
   backups without these additive structures migrate normally. Legacy policy
   snapshots are labelled. Reply cycles reconstructed from durable activity are
   labelled legacy and omit unproven historical ownership; missing evidence is
   never replaced with fabricated events.
4. Recreate the application and verify that configuration, branding and policy
   references survive. Rehearse the previous digest with outbound work held,
   then return to the candidate. Startup refreshes projections whose stored case
   version is stale without changing their assigned policy. If the previous image
   edited the active policy, a fresh snapshot is used for future cases instead of
   silently reusing an outdated version reference.
5. Reconcile submitting/unknown outbound runs before allowing dispatch in a
   restored instance. Completed runs retain their identifiers and must not be
   submitted again. Preserve the previous image digest and matching configuration
   alongside the encrypted, restore-checked backup.
6. Publish the exact corresponding AGPL source and deploy only the scanned image
   digest. Verify sign-in, support identity, branding, plan limits, domain health
   and monitoring. Rollout to any additional instance is a separate operation.


### Inbox review regression gate

The managed release must cover membership revocation in routing, assignment
options, preflight, capacity and workload queries. Keep soft-deleted organization
members out of candidate lists even when old team memberships remain.

Verify quiet-conversation response-target refresh without a case-version change;
report scope/loading and failed-lookup recovery; post-save policy draft isolation;
preset discard protection and removal; combined condition budgets; specific field
validation; shift-specific accessible names; text-entry shortcut boundaries; and
workload for owners without teams. Include a synthetic 1,000-case queue/report
measurement without interpreting local timings as a production capacity promise.

Inspect advanced-filter summaries, page selection, agent search, calendar-aware
snooze suggestions and export with unapplied filters at desktop/mobile widths in
both themes. Keep synthetic messaging dispatch held through restore and rollback.


### Historical case and preview regression gate

Verify exact-case metadata, policy and field reads for historical links, including
archived recorded values. Historical screens must be explicitly read-only and
must not silently operate on the latest case. Current writes retain case-version
and subject checks. Exercise cursor paging beyond 50 activity entries with tied
timestamps and concurrent new activity, and retain older loaded pages on refresh.

Test stable historical-report facets, scoped literal queue search, masking against
phone-content inference, dry-run selection beyond page one, and keyboard focus
through macro preview loading. Cover empty versus required-details resolution,
structured field errors, stale versions, revoked permissions and uncertain network
results without partial writes or draft loss.

Keep a database query-count regression for twenty repeated and distinct custom
field conditions: request-local batch evaluation must remain equivalent to the
bounded all/any predicate semantics without cross-organization cached data. Run
all `inbox-*.spec.ts` browser workflows and inspect both themes and mobile widths.
