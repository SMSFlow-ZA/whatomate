# Managed plan enforcement

This AGPL fork adds host-controlled allowances to upstream
`v0.1.1-beta-0.17`. Public billing and customer administration cannot change the
allowances. Existing unmanaged deployments retain upstream behavior.

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
`users`, `numbers` or `storage`. Reserved support changes use HTTP 403 and
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
docker build -f docker/Dockerfile.managed --build-arg VCS_REF="$(git rev-parse HEAD)" -t whatomate-managed .
```

The managed Dockerfile retains the pinned upstream runtime and replaces the
application binary and embedded frontend. Production deployments should pin the
derived image digest and the same public source revision. Before deployment run
`go test -p 1 ./...` with disposable PostgreSQL and Redis, plus frontend type
checks, unit tests and production build. Upstream packages share a test database
and must run serially; quota tests themselves exercise concurrent requests.
