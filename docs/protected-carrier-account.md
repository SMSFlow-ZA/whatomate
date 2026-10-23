# Offline carrier account owner operations

`cmd/extensions-carrier-account` is an owner-operated offline command, not an API
or runtime worker. It never migrates schema, changes grants/roles/passwords/MFA,
subscribes an app, registers a phone, calls Meta, or starts listeners/workers.
Default provisioning always creates `inactive`; default incoming/outgoing, auto-read receipts,
business calling and SMB flags are false. Activation and carrier configuration
remain separate reviewed owner operations. The explicit `-activate` selector
described below can change an existing account's database status; it cannot
enable runtime carrier configuration or release dispatch.

Build with the same reviewed `internal/protectedschema.ReleaseIdentity` and
`ExpectedCatalogSHA256` linker pins as the compatible protected release. Empty
pins, caller-substituted config pins, mismatching latest seal, pending schema
attempt, missing protected marker or changed physical catalog refuse. No seal
or marker is created or updated. The default unpinned development build refuses.

```sh
extensions-carrier-account -config /root/owner.toml \
  -plan /root/new-account.json -ca-file /root/database-ca.pem \
  -attempt /root/new-account-attempt.json
# Explicit reviewed apply, with stopped runtime and shared private lifecycle lock:
extensions-carrier-account -config /root/owner.toml \
  -plan /root/new-account.json -ca-file /root/database-ca.pem \
  -attempt /root/new-account-attempt.json -apply
```

All inputs must be absolute Linux root-owned regular files, mode0600, with one
link and no symlink components. Ancestors must be root-owned and not writable by
group/others. Use a private root0600 CA copy; this command deliberately does not
accept a public-mode CA input. Windows execution refuses. No passwords, keys,
tokens or DSNs are accepted as command flags. TOML is loaded from its file only,
without config environment providers. PostgreSQL credentials, connection identity,
TLS roots/hostname and runtime parameters are set explicitly; TLS verify-full is
mandatory with no plaintext fallback. Owner config needs production environment,
protected externally managed schema, dispatch stopped, carrier disabled, no
synthetic-development mode and an existing encryption key of at least32 bytes.

Strict plan JSON has exactly these supported fields (no defaults for identities,
no caller-selected status or flags):

```json
{
  "schema_version": 1,
  "organization_id": "11111111-1111-4111-8111-111111111111",
  "account_id": "22222222-2222-4222-8222-222222222222",
  "operator_id": "33333333-3333-4333-8333-333333333333",
  "name": "Dedicated carrier",
  "app_id": "123456789",
  "phone_id": "234567891",
  "business_id": "345678912",
  "api_version": "v26.0",
  "access_token": "PRIVATE_CURRENT_ACCESS_TOKEN",
  "app_secret": "PRIVATE_CURRENT_APP_SECRET",
  "webhook_verify_token": "PRIVATE_EXISTING_VERIFY_TOKEN"
}
```

The example is not deployable. API version must be an explicitly reviewed value;
the command does not upgrade it. Access token and app secret use the same
`crypto.EncryptFields` AES-GCM format as account creation. The account model stores
its verification token in the existing verification column; this command does
not change that storage contract. Never archive the plan or account response.

Preview uses read-only SQL transactions and makes no attempt/account/audit writes.
Apply first reserves and fsyncs a create-only private local attempt, then obtains
the existing protected-schema exclusive advisory fence on the transaction's
physical connection. Running guarded runtime sessions cause immediate refusal.
All approved owner operations must additionally share the deployment lifecycle
lock; this CLI cannot enforce a private host lock across arbitrary operators.
The database name, current_user/session_user and database owner must match the
trusted owner config. The exact organization and active primary-organization
operator are checked and locked through commit. The physical catalog is checked
against the compiled pin while the owner transaction retains its fence.

For provisioning, any existing UUID, phone ID, WABA or organization/name match, including deleted
rows, refuses. This intentionally supports one dedicated account for a WABA;
it cannot add a second number/account to an already represented WABA. Account
insert and sanitized `audit_logs` insertion commit together. Existing account
defaults and records remain unchanged. Output contains only account/org UUID,
inactive status, applied boolean and provider_calls=false.

The attempt is retained on success as well as refusal, crash or unknown commit.
It is not a success receipt and contains no credentials. Never delete it to retry.
Inspect the database and audit record through an authorized read-only owner path
before explicit reconciliation. A failed stdout write after commit is also an
unknown outcome to the caller. Provisioning has no automatic retry or update/upsert.

## Explicit inactive-to-active owner operation

`-activate` selects a separate operation and defaults to a read-only preview.
Omitting it preserves the provisioning preview/apply behavior and output. It uses
the same strict private plan, owner configuration, TLS verification and compiled
schema pins; no new plan fields, status defaults or environment overrides exist.

```sh
# Review only: the existing row must match and still be inactive.
extensions-carrier-account -activate -config /root/owner.toml \
  -plan /root/existing-account.json -ca-file /root/database-ca.pem \
  -attempt /root/activation-attempt.json
# Separate explicit apply, with runtime stopped and private lifecycle lock held:
extensions-carrier-account -activate -apply -config /root/owner.toml \
  -plan /root/existing-account.json -ca-file /root/database-ca.pem \
  -attempt /root/activation-attempt.json
```

The exact organization/account/name/app/phone/WABA/API-version tuple must already
exist, be nondeleted and have status `inactive`. The organization must exist and
the operator must be active, nondeleted and belong to that primary organization.
Stored access token and app secret must use authenticated `enc:` ciphertext,
decrypt using the existing private encryption key and match the plan. The stored
verification token must also match. Plaintext, corrupt or mismatching stored
credentials refuse; nothing is refreshed or reencrypted. These comparisons prove
agreement with private custody, not token validity at Meta.

Both activation preview and apply require the protected-schema exclusive advisory
fence, so guarded running runtime sessions refuse. Preview remains a read-only
transaction with no attempt, account or audit writes. Apply reserves/fsyncs the
create-only attempt before SQL, then uses one serializable owner transaction to
verify compiled latest seal, physical catalog, pending attempts and protection;
lock the organization, operator and exact account; and compare-and-set only the
inactive status to `active` with update attribution/time. A sanitized status-change
audit commits in that same transaction. Audit failure rolls back activation.
Credentials, default flags, grants, MFA, subscriptions, provider/config/network
state, schema and dispatch remain unchanged.

An active account, including one activated by a concurrent operation, refuses;
this is not an idempotent upsert or activation retry API. Every apply attempt is
retained on success, refusal, rollback or unknown outcome. A lost commit response
or failed stdout write requires private account/audit inspection; do not delete
the attempt or replay with a new path. The result has the existing sanitized
shape, desired `status: "active"`, `applied` and `provider_calls: false`.
`applied: false` is the preview result, not proof of activation. The tool does not
start listeners/workers, prove signed webhook routing or enable outbound traffic.

Focused tests require `EXTENSION_ADMIN_TEST_DSN` pointing to disposable PostgreSQL.
They create/drop only random `carrier_account_*` schemas, never a whole database,
and fail if the test DSN is missing. They verify encrypted roundtrip, inactive
flags, read-only preview, audit rollback, duplicate/concurrent refusal, runtime
lease fencing, seal/pending/catalog boundaries and private input checks. These
fixtures are not target TLS, live token or Meta routing acceptance.

Activation tests additionally cover exact tuple and encrypted-custody refusal,
already-active/deleted accounts, inactive/foreign/deleted operators, stopped-runtime
fencing (including preview), physical-catalog and seal/pending/marker drift,
one-winner concurrent activation, status-only mutation and atomic audit rollback.
They use genuine disposable PostgreSQL transactions and race instrumentation;
they do not reproduce a production network's ambiguous commit outcome.
