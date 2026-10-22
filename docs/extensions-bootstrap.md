# Offline initialization of a new protected database

Build the separate `./cmd/extensions-bootstrap` binary from the same reviewed
host/add-on source pair as the application. This is an operator tool, not a server
subcommand. It does not import the host handlers, initialize channels or model
providers, connect to Redis, accept HTTP, or start background workers.

`whatomate server -migrate -workers 0` is **not** an offline substitute: that
command starts HTTP, the SLA processor and independent extension/channel workers.

## Operator boundary

Use only a newly provisioned, dedicated PostgreSQL database, held inaccessible to
all application/processing writers and other operators during initialization.
Provision the scoped schema-owner role and verified PostgreSQL TLS separately.
No superuser, CREATE EXTENSION, role grants, cloud operations or database creation
are performed by this tool. An administrator may preinstall extensions such as
pgvector; extension-owned objects are excluded from the emptiness check.

Run the Linux tool as root with a regular, root-owned, owner-only TOML file
(normally mode `0600`), mounted read-only from guarded persistent storage. Never
put passwords on the command line. It reads configuration only from that file;
`WHATOMATE_*` overrides and ordinary default credentials are deliberately ignored.
The database fields must explicitly supply host, port, database name, user,
password and `ssl_mode = "verify-full"`. Configure the trusted PostgreSQL CA using
the deployment's reviewed libpq/pgx TLS trust mechanism, e.g. a read-only
`PGSSLROOTCERT` file. The database hostname must match its certificate.

The file must also explicitly set:

```toml
[app]
environment = "production"
debug = false

[channels]
stop_dispatch = true

[extensions]
protected_mode = true
synthetic_development = false
carrier_enabled = false
carrier_dispatch_enabled = false
```

Carrier binding arrays must be empty. Supply `[default_admin]` email, full name
and a privately generated 20–72 byte password. The tool creates the existing core
default administrator role/superadministrator semantics; it does not invent
employee identity links, extension grants, operator profiles, MFA enrollment,
recovery codes or verified sessions.

Invoke once, using the exact database name from the private configuration:

```sh
extensions-bootstrap -config /run/secrets/bootstrap.toml \
  -database NEW_DATABASE_NAME -organization-name 'Reviewed organization name'
```

In a container, use only the reviewed tools image and an isolated internal
network containing the dedicated PostgreSQL service. Publish no ports, attach no
external network, mount no Docker socket, and mount only the private configuration
and PostgreSQL CA read-only. Do not reuse the application server entry point.
The root operator owns actual container/network creation and verifies isolation.

## Failure and completion semantics

The tool checks the actual database name and all user namespaces, refusing any
non-extension-owned relation, routine, enum or domain, including an existing
protected-host marker. A database-wide advisory lock remains on the same physical
connection while schema owners use other connections. Lock ownership is checked
between owner migrations and before final activation. The lock coordinates this
bootstrap tool; it does not replace exclusive database access controls.

A durable attempt sentinel is committed before migration. Core schema, default
organization/admin and organization naming share one transaction. Add-on schema
owners use their existing migration APIs and separate transactions. After exact
identity/membership and empty MFA/grant/transport state verification, protection
guards, the irreversible protected-host marker and completion flag commit in a
final transaction. A fresh read verifies the marker before success is reported.

This is **not an atomic transaction across all owners**. Any failure, termination,
timeout or unknown commit leaves the database held for private inspection. There
is no automatic retry, cleanup, deletion, resume or fallback to ordinary mode.
Even a core rollback retains the attempt sentinel and refuses another attempt.
An authorized operator may explicitly recreate only the verified new isolated
database after inspection; never erase or reuse a prior/customer/protected DB.

Successful stdout contains core migration progress followed by a JSON object with
opaque organization/admin UUIDs and `protected=true`, `dispatch_held=true`,
`runtime_ready=false`, `mfa_enrolled=false`. No credentials or private source text
are printed. Failure diagnostics are intentionally generic; preserve isolation
and inspect the dedicated DB through the approved private administration path.

Success proves schema/initial identity and the storage boundary only. It does not
certify production provider settings, vector extension/profile readiness, a worker
indexer grant, processing availability, runtime grants, backup/recovery, ingress,
MFA or carrier acceptance. Those remain separate reviewed deployment gates. Never
boot an ordinary/legacy image against this protected database.

## Targeted verification

`BOOTSTRAP_TEST_DATABASE_URL` points only to the disposable PostgreSQL test server.
Tests create/drop their own random schemas; production has no schema-override
flag and checks every user namespace. Run `go test -race
./cmd/extensions-bootstrap` and `go vet ./cmd/extensions-bootstrap` with the paired
add-on source. No production database or network provider is used by these tests.
