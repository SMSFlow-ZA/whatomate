# Production document controller

Build the Linux `./cmd/extensions-processing` command from the same reviewed host
and add-on revisions. It is a separate, privileged operator process. The HTTP app
receives neither its Docker access nor its private database credential. This does
not approve the current scanner image, definitions, deployment or resource fit.

Run `extensions-processing -config /private/processing-controller.json -check`
to verify actual current prerequisites without claiming work. Omit `-check` for
the single-lane service, or use `-once` for at most one durable claim. There is no
synthetic environment escape, automatic migration, download or requeue command.
Errors never print private configuration, DSNs, input text or worker diagnostics.

The strict, root-owned mode-600 JSON has these fields:

| Field | Required value |
| --- | --- |
| `InstanceID`, `OrganizationID` | Exact existing UUIDs; dedicated one-organization installation |
| `WorkerUserID` | Explicit existing active worker user UUID, never an inbound actor |
| `ProfileID` | Exact profile applied by `extensions-admin`, kind `knowledge-worker` |
| `InstanceRoot` | `/srv/<reviewed-mount>/er/<instance-UUID-hex>` |
| `DatabaseName`, `DatabaseRole` | `er_<hex>` and `er_processing_<hex>` |
| `DatabaseDSNFile` | `<InstanceRoot>/config/processing.dsn`, root-owned mode 600 |
| `Audiences` | Explicit nonempty audience allowlist without wildcards |
| `AuthorizationFunctionSHA256` | Independently approved SHA-256 of the exact installed `pg_get_functiondef`; mandatory at normal startup |
| `DefinitionsVerifiedAt`, `DefinitionsValidUntil` | Reviewed UTC acceptance window, no more than 24 hours; at least seven minutes remaining |
| `DefinitionsReviewSHA256` | Actual separately retained definition-review evidence SHA-256 |
| `Supervisor` | Existing `documents.SupervisorConfig` shape from add-on document-worker documentation |

`Supervisor.JobRoot` is exactly `<root>/jobs`, `DefinitionsRoot` is exactly
`<root>/definitions`, `DockerPath` is `/usr/bin/docker`, `MaxConcurrent` is one,
and `Worker.SignaturesValidUntil` equals the accepted deadline. Other bounds,
pinned image, scanner/extractor/OCR revisions and fixed in-worker paths must pass
`documents.NewSupervisor`. All persistent paths must actually be on NFS, have no
symlink ancestor or writable-by-other directory, and retain the same root inode.
The job root must also pass the supervisor's private directory requirements.

The private DSN must explicitly select that database/user and a password with
verified TLS (normally `sslmode=verify-full` and a private trusted CA). It is never
accepted through process arguments or environment. The connected role must have
no superuser, create-database, create-role, replication, bypass-RLS or membership
privileges; it must not own public tables, read user password hashes or access
generic transcripts/accounts/contacts. Provision only function EXECUTE and knowledge queue SELECT/UPDATE for the worker.
It receives no host identity/grant SELECT or UPDATE authority. Normal worker
startup creates no roles, grants or functions.

An explicitly audited current `extensions-admin` replacement is mandatory:
the current access version's audit must identify kind `knowledge-worker` and the
configured profile. The current custom role is restricted to `knowledge:job` and
optionally `knowledge:read`; every job audience must be in both private policy
and current `knowledge:job` grants. Human/operator, legacy grant, admin/superadmin
or invented MFA/session state cannot serve as this worker principal.

The authorized queue scopes selection and expired-lease marking to its tenant.
It calls the host owner validator within the claim and artifact-completion SQL
transactions. The host rechecks the immutable protected marker, active primary
membership, role and grants, sharing the administration lock through commit. Host row locks are taken only
inside the reviewed fixed SECURITY DEFINER owner function; PostgreSQL row-lock
privileges must never be solved by granting core UPDATE to the worker.
Revocation that commits first blocks completion. A concurrent revocation waits
until an already authorized artifact transaction commits. Role and user rows are
locked too. Safe failure/unknown marking remains possible after grant revocation;
it cannot publish content or requeue. There is no approval/publication operation.

Definition acceptance is deliberately strict: the frozen directory contains only
`main.cvd`, `daily.cvd` and `bytecode.cvd`, with the reviewed corpus digest. Every
claim rechecks that digest and runs the pinned image's actual isolated
`sigtool --info` for all three files, requiring successful exit and `Verification
OK.`. For the reviewed ClamAV 1.4 interface this verifies the embedded checksum and
digital signature; see [official CVD documentation](https://docs.clamav.net/manual/Development/libclamav.html).
`.cld` incremental corpora, local `.ndb` signatures, detached-signature format
changes and arbitrary extension of expiry are refused. Renew acceptance through
an independently verified frozen snapshot and reviewed private configuration,
never by a runtime download or synthetic one-hour window. The existing local
ClamAV 1.4.4 image is not thereby approved for production or current support.

The controller holds a nonblocking OS lock in its private job root for its full
lifetime. Queue leases and permanent supervisor job directories survive crashes;
expired leases become unknown and no restart clears them. Any uncertain or failed
claim stops this process; operators inspect owner state before restarting. SIGTERM
cancels the current sandbox with the supervisor's bounded cleanup. The processing
systemd unit must keep `Restart=no` and allow cleanup before killing the process.

Restored receipt files, unresolved source quiescence, absent NFS, expired/corrupt
definitions or revoked access stop new processing. No command clears holds.
Deployment must connect the processing unit to the real mount lifecycle and
authorize normal startup through the guarded private launcher. A privileged
administrator bypassing these deployment controls remains outside this fence.

Resource admission must include the controller and its separately bounded Docker
worker. Streaming definition hashing has bounded allocation with unchanged corpus
identity; that does not prove the complete PDF/OCR/artifact path fits 128/192 MiB.
Measure maximum allowed clean and OCR jobs, cancellation and concurrency before
selecting the controller cap, worker limits and host headroom. No production
scanner run, capacity approval or real-document processing was performed here.

## Explicit owner-function installation

Before normal startup, emit a **new** root-private migration file with
`extensions-processing -config /private/processing-controller.json -authorization-sql /private/new-owner-function.sql`.
This mode never connects to PostgreSQL or applies SQL. The configuration may omit
`AuthorizationFunctionSHA256` in this emission-only mode to avoid bootstrap
circularity. Normal `-check`, `-once` and service startup require the real approved
hash; there is no hash default or runtime bypass. Inspect the entire generated
SQL independently before a privileged operator applies it in the exact database.

The SQL creates a dedicated `er_processing_owner_<instance-hex>` NOLOGIN role
with no membership, superuser/create/replication/bypass privileges. It grants only
needed column reads and UPDATE on one key column of each row-locked table;
PostgreSQL requires some UPDATE privilege for `FOR SHARE`. That authority stays
behind the fixed function and is never granted to the worker or another role.
The owner temporarily receives schema CREATE to assign function ownership, then
loses it. The function is `public.er_processing_authorize_v1(uuid,uuid,text,text[])`,
with `SECURITY DEFINER SET search_path=pg_catalog`, explicitly qualified tables,
no dynamic SQL and pinned installation identities/audiences. Its default PUBLIC
execution is revoked; only the exact worker role receives EXECUTE. Do not grant
membership in the owner role to any account, or make it a login role.

Apply only through the separately approved privileged migration workflow. Then
retrieve `pg_catalog.pg_get_functiondef('public.er_processing_authorize_v1(uuid,uuid,text,text[])'::regprocedure)`
as exact UTF-8 bytes, independently review that definition and the owner/ACL,
compute its SHA-256 and place the actual value in the root-private configuration.
Do not hash a psql table display, newline-altered copy or invented placeholder.
The worker independently checks the exact signature, owner identity and safety,
function ACL, `pg_catalog`-only proconfig and definition hash in every claim/commit
transaction. Replacing the function or policy requires a new privileged review;
ordinary startup cannot recreate it or broaden grants.

Actual PostgreSQL tests use a genuine nonowner LOGIN worker with queue privileges
and owner-function EXECUTE only. They prove restricted-role startup, claim and
artifact completion, revocation serialization, rejection after superadmin
promotion and rejection of column-only transcript grants. These are synthetic
SQL boundary fixtures, not production scanner or target installation acceptance.
