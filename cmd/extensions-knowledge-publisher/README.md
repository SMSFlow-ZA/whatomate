# Offline synthetic knowledge publisher identity

This command creates one clearly labelled **automation synthetic** primary identity
in an existing protected database. It does not publish a document and is not
human, employee, client or legal approval evidence. It never changes the existing
owner, grants a role, links an employee, enrolls MFA, creates an API key, migrates
schema or calls a messaging/model provider.

Default is a read-only preview. Apply requires a root-private exact plan, owner
TOML and trusted database CA. The executable must be built with the existing
protected release/catalog linker pins. PG environment defaults are refused;
configuration is parsed without application environment overrides. TLS is
verify-full without fallback. The transaction retains a shared migration lease
on its physical connection, verifies the exact derived database/owner, current
release/catalog/generation, protected state, absent pending installation and the
actual catalog. It remains compatible with a healthy running application.

The identity plan fixes instance, organization, active operator, new user and
membership UUIDs, generation/release/catalog, and purpose
`automation-synthetic-knowledge-publisher`. Only email addresses matching
`synthetic-knowledge-publisher-<8 lowercase hex>@example.invalid` are accepted.
Any existing UUID/email/membership, protected grants, identity links, MFA or API
key state refuses creation. The inserted user is active, unavailable for routing,
not a superadministrator, has no role and is visibly named
`AUTOMATION SYNTHETIC KNOWLEDGE PUBLISHER`. A default same-organization membership
and an automation-only creation audit are inserted atomically.

```
extensions-knowledge-publisher -config /private/owner.toml \
  -ca-file /private/postgres-ca.pem -plan /private/identity-plan.json \
  -attempt /private/new-attempt/attempt.json \
  -password-out /private/new-attempt/password
```

Review preview and exact bytes before adding `-apply`. Apply reserves a fresh
attempt and saves a cryptographically random primary password as a create-only
root-private 0600 file. No password or password hash appears in output or audit.
Attempt/password files are retained after every failure. Any ambiguous result
requires private state inspection; never rerun or overwrite an attempt.
The containing path must already be root-owned, free of symlinks and untrusted
writable ancestors. Linux is required for custody operations.

After creation, use the existing `extensions-admin` inspect/preview/apply path to
replace this identity's empty authorization with **only** `knowledge:read` and
`knowledge:publish`, each for the exact approved audience. Use its actual observed
role/access version, not an assumed value. Give no upload, review, retire, case,
AI, security or generic-administration authority. A different authorized actor
reviews the exact document manifest. The synthetic publisher authenticates through
the normal signed primary-session path; no fabricated claims or API key bypass.
Its publication is automated synthetic demonstration evidence, never SANBS human
approval. Existing two-person and current-audience checks remain controlling.

Focused tests require a disposable PostgreSQL database named from the synthetic
fixture instance, owned by its derived owner, through `PUBLISHER_TEST_DSN`.
Tests fail rather than silently skip without that fixture. Private-file tests
also require a writable private `/root` in the disposable Linux test container.
Tests use only synthetic records and passwords, with no target or provider call.
