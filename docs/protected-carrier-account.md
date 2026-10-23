# Offline inactive carrier account provisioning

`cmd/extensions-carrier-account` is an owner-operated offline command, not an API
or runtime worker. It never migrates schema, changes grants/roles/passwords/MFA,
subscribes an app, registers a phone, calls Meta, or starts listeners/workers.
The account is always `inactive`; default incoming/outgoing, auto-read receipts,
business calling and SMB flags are false. Activation and carrier configuration
remain separate reviewed owner operations. Protected carrier startup requires an
active account, so this command alone cannot activate the carrier.

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

Any existing UUID, phone ID, WABA or organization/name match, including deleted
rows, refuses. This intentionally supports one dedicated account for a WABA;
it cannot add a second number/account to an already represented WABA. Account
insert and sanitized `audit_logs` insertion commit together. Existing account
defaults and records remain unchanged. Output contains only account/org UUID,
inactive status, applied boolean and provider_calls=false.

The attempt is retained on success as well as refusal, crash or unknown commit.
It is not a success receipt and contains no credentials. Never delete it to retry.
Inspect the database and audit record through an authorized read-only owner path
before explicit reconciliation. A failed stdout write after commit is also an
unknown outcome to the caller. No automatic retry or update/upsert exists.

Focused tests require `EXTENSION_ADMIN_TEST_DSN` pointing to disposable PostgreSQL.
They create/drop only random `carrier_account_*` schemas, never a whole database,
and fail if the test DSN is missing. They verify encrypted roundtrip, inactive
flags, read-only preview, audit rollback, duplicate/concurrent refusal, runtime
lease fencing, seal/pending/catalog boundaries and private input checks. These
fixtures are not target TLS, live token or Meta routing acceptance.
