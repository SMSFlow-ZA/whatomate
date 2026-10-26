# Offline synthetic role-acceptance identities

This standalone owner tool creates exactly six new synthetic primary identities:
`employee`, `manager`, `hrbp`, `er`, `leadership`, and `unauthorized`. These are
fixture labels, **not assigned roles or real employee identities**. Every new
identity starts active, unavailable for routing, non-superadmin, with an empty
role and no grants, employee links, MFA, API keys or SSO. Its visible name begins
`SYNTHETIC ROLE ACCEPTANCE:` and its email is restricted to
`synthetic-role-<fixture>-<8 lowercase hex>@example.invalid`.

The exact plan fixes version 1, purpose `synthetic-role-acceptance`, a fresh
32-character lowercase hexadecimal `attempt_nonce`, current instance,
organization, active operator, generation, release and catalog SHA256. Its
`identities` array must contain the six labels above in that exact order, each
with a new canonical `user_id`, `membership_id`, and reserved email. All UUIDs
must be distinct, including the existing instance, organization and operator.
Unknown fields and duplicate JSON keys at every nesting depth are rejected.
The command computes `plan_sha256` from the actual private plan bytes.

```sh
extensions-demo-identities -config /private/owner.toml \
  -ca-file /private/postgres-ca.pem -plan /private/fixture-plan.json \
  -attempt /private/new-run/attempt.json \
  -password-out /private/new-run/passwords.json
```

Default is a read-only database preview. Adding `-apply` reserves an immutable
attempt and creates a root-private JSON file mapping fixture labels to generated
password strings. No password or password hash is printed or audited. Linux
root, absolute trusted paths, private single-link regular files and create-only
publication are required. PostgreSQL environment defaults are refused; TOML is
parsed without application environment overrides. The connection verifies TLS
hostname/CA, uses TLS 1.2 or newer, and has no plaintext fallback.

One serializable transaction holds the shared schema migration lease and checks
the exact derived database owner/current session, current seal, actual physical
catalog, protected state, absence of pending migrations, and active same-org
operator/default membership. Creation locks that operator/membership and inserts
all six users, default memberships and creation audits atomically. Existing
quota triggers remain in force; any failure rolls back the entire batch. No
schema/role/permission installation or existing identity update occurs.

Only exact post-commit readback reports `committed-observed`. An ambiguous COMMIT
or readback is `unknown-held`, never retried. Preserve all files and use
`-reconcile` with the original arguments for read-only verification of the
original attempt, raw plan hash, nonce/audits, password hashes, identities,
memberships and empty authority. Reconciliation is deliberately refused after
grants or other identity state have changed. It never repeats creation.

Existing `extensions-admin` separately inspects and applies reviewed scopes using
the actual access version/current role. Employee self-service additionally
requires that tool's genuine approved-roster identity binding; this command does
not invent one. Cleanup first logs out fixture sessions and uses the existing
versioned revoke operation; this command never deletes/deactivates identities or
resets factors/passwords. Review capacity in `whatomate_plan_state` before use;
the six fixture users count toward normal managed user quotas.

Focused tests require disposable `DEMO_IDENTITIES_TEST_DSN`, with the fixed
synthetic derived owner/database. Each test uses a unique schema and removes only
that schema. Private-file tests require isolated Linux root with private `/root`.
Tests cover strict plans, preview, atomic six-user creation, empty authority,
tenant/new-only limits, duplicate email concurrency, genuine trigger rollback,
read-only reconciliation, and connection loss during COMMIT. The default-deny
test uses the trusted add-on actor boundary; it is not hosted login/browser proof.
No customer/production credentials or provider calls are used.

The dedicated TLS test additionally requires `DEMO_IDENTITIES_TLS_TEST_DSN`,
`DEMO_IDENTITIES_TLS_CA_FILE`, `DEMO_IDENTITIES_TLS_WRONG_CA_FILE`, and
`DEMO_IDENTITIES_TLS_PASSWORD_FILE`. It requires the explicit
`DEMO_IDENTITIES_TLS_PUBLIC_EMPTY=true` acknowledgement because it creates and
removes fixture tables in the disposable database's otherwise empty `public`
schema. Never provide an existing/shared application database. It exercises the
actual CLI parsing/custody/connection path over hostname-verified TLS for preview,
create and reconciliation, and rejects a different CA and hostname. Without that
explicit disposable fixture, this one test is an opt-in skip.
