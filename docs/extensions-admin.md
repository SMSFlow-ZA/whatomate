# Offline ER operator onboarding

`cmd/extensions-admin` provisions authorization for exact existing, active users
in one existing organization. It works while generic host user/role APIs are
denied by the protected-host boundary. It creates no primary identities and
never reads or changes passwords, authentication stamps, MFA keys, recovery
codes, sessions or administrator status. An infrastructure administrator gains
no ER access unless the private policy explicitly names that user and profile.

Build the tool from the same reviewed host/add-on source as the server:

```sh
go build -o extensions-admin ./cmd/extensions-admin
```

Use a private, deployment-owned policy file and the trusted host TOML config.
Keep these outside the public source tree. Mount the policy read-only to the
operator process; possession of this tool and database write credentials is an
administration capability, separate from browser authentication. The JSON plan
cannot supply an operator identity, permissions, unit/audience grants or MFA
assertions. Unknown JSON properties and trailing JSON values are rejected.

Host migrations must already have run. Inspection and the default preview do
not create tables, change grants or write audit rows. They require the same
current active membership checks as apply. Both configured operator and target
must have the configured organization as their primary organization; a
superadministrator's access to other organizations does not satisfy this rule.
Inspection uses a database read-only transaction. Preview uses a rollback-only
transaction so it can exercise the same directory shared-row proof lock as
apply; no mutation branch runs and the preview transaction never commits.

The following identifiers and unit are synthetic examples, not deployment
configuration:

```json
{
  "organization_id": "11111111-1111-4111-8111-111111111111",
  "operator_id": "22222222-2222-4222-8222-222222222222",
  "nonemployee_operators": {
    "33333333-3333-4333-8333-333333333333": "reviewed-external-operator-17"
  },
  "profiles": {
    "north-triage": {
      "kind": "operator",
      "subject_kind": "nonemployee",
      "user_ids": ["33333333-3333-4333-8333-333333333333"],
      "permissions": [
        "security:mfa.enroll", "cases:read", "cases:read.triage",
        "cases:assign.triage"
      ],
      "grants": [
        {"permission": "cases:read.triage", "unit_id": "north"},
        {"permission": "cases:assign.triage", "unit_id": "north"}
      ]
    }
  }
}
```

Only the tool's finite ER permission allowlist is accepted. All scopes must be
exact; wildcards, duplicate grants and scopes outside the named profile are
rejected. Knowledge and AI use audience scopes; other supported permissions use
unit scopes. Human profiles may explicitly include `security:mfa.enroll` for
normal authenticated self-enrollment. MFA revocation, emergency/break-glass,
generic administration and privacy/restore administration are outside this tool.
The tool never enrolls a factor or converts a primary session into MFA.

Human operator profiles have no default identity classification. The example
explicitly provisions an attested external/nonemployee operator, named in the
separate `nonemployee_operators` allowlist with its reviewed reference. Such a
profile cannot grant employee own-case read/submit permissions and is refused
when that account already has employee identity links. The classification and
reference are audited. Do not classify employee operators as external to make
bootstrap succeed.

An `"employee"` subject classification requires a separate, deployment-owned
`employee_bindings` entry for each named user. The binding must come from the
operator's verified account-to-employee process, and must pin employee ID,
canonical phone and the exact approved directory version. Names and email
similarity are never used to infer identity. The grant plan cannot supply or
override a binding. For example, a separately permitted employee profile for
the following synthetic user would use:

```json
{
  "employee_bindings": {
    "55555555-5555-4555-8555-555555555555": {
      "employee_id": "SYNTHETIC-17",
      "phone": "+27820000017",
      "expected_roster_version": 1
    }
  }
}
```

The directory owner's `ValidateIdentityTx` locks its current snapshot `FOR SHARE`
inside the grant transaction. It requires a matching directory owner audit,
exact version, current configured freshness, canonical phone and the same
active, nonrevoked employee ID. Approval, replacement and revocation update that
same row and must wait until the grant/link/audit transaction finishes. A stale
or conflicting snapshot cannot authorize a grant. The AccessStore then appends
the verified identity through `LinkIdentityTx` in that same transaction; an
employee already linked to another user or a user with cross-organization
identity links is refused. Existing historical identities remain linked for
case-conflict exclusion, including after access revocation. A failed role,
grant or audit write rolls back the new link too.

The private policy contains the canonical phone; CLI output and the access audit
contain only the approved roster version and a SHA-256 reference to the
organization/user/employee identity tuple. Neither prints or audits the phone.
Updating the approved roster requires a freshly reviewed policy version before
further employee provisioning. Inspection and access revocation do not require
an active roster, so administrators can still remove access after an employee
becomes ineligible.

Worker profile kinds are separately constrained: `routing` and `escalation`
permit case work/triage only, `directory-worker` permits directory read/import,
and `knowledge-worker` permits knowledge read/job access. They cannot acquire
human approval or MFA enrollment. Routing/escalation identities must also appear
in the corresponding trusted host configuration lists. Directory/knowledge
worker supervisors must separately bind their configured identity; granting a
profile does not create or start a worker. Every such principal must still be
an existing active member, and runtime authorization still checks current grants.

Inspect the exact permitted user to obtain its current role and access version:

```sh
extensions-admin -config /run/config/host.toml -policy /run/private/er-policy.json -inspect 33333333-3333-4333-8333-333333333333
```

A replacement plan includes both expected access version and current role ID
from inspection (`expected_role_id` is an empty string only if no role exists):

```json
{
  "organization_id": "11111111-1111-4111-8111-111111111111",
  "user_id": "33333333-3333-4333-8333-333333333333",
  "action": "replace",
  "profile": "north-triage",
  "expected_version": 0,
  "expected_role_id": "44444444-4444-4444-8444-444444444444",
  "reason": "approved-access-review-17"
}
```

Preview, review the exact resulting permissions/scopes and document hashes,
then apply the same files explicitly:

```sh
extensions-admin -config /run/config/host.toml -policy /run/private/er-policy.json -plan /run/private/grant-plan.json
extensions-admin -config /run/config/host.toml -policy /run/private/er-policy.json -plan /run/private/grant-plan.json -apply
```

Replacement assigns a new, uniquely named role to this user and replaces the
complete scoped-grant set. Existing generic and ER permissions are not merged.
Existing shared roles and other users are untouched. Old roles are retained for
review rather than automatically deleted. Revocation uses `"action":"revoke"`
and `"profile":""` with freshly inspected expected version/role; it assigns an
empty role and removes every scoped grant. It does not disable the primary
account or revoke an enrolled factor. In-flight protected operations continue
to rely on their current authorization rechecks.

`api.AccessStore.ReplaceVersioned` in the add-on dependency owns the transaction:
it serializes changes for the exact organization/user, checks expected version,
runs the trusted host membership/role callback, replaces grants, advances
`addon_api_access_versions` and appends `addon_api_access_audit`. Host role
assignment, membership-role synchronization, additive employee identity and the
audit commit together.
The audit retains named operator, reason/reference, previous/new versions,
before/after scopes, profile, role IDs, permissions and policy/plan SHA-256 hashes.
Audit UPDATE, DELETE and TRUNCATE are refused. Keep reasons free of case content.
Database privileges, retention, off-host audit preservation and access review
remain deployment responsibilities.

Legacy trusted `AccessStore.Replace` remains available for existing fixtures and
integrations, but now advances the same version and appends a clearly labeled
`legacy-replace` audit. It is not the production onboarding interface. A version
or role conflict requires a fresh inspection and reviewed plan. An unknown
commit result requires reconciliation of current state and audit; do not retry
the old plan blindly.

The CLI does not infer account-to-employee identity equivalence. Verify private
policy bindings before human case access; do not invent roster aliases to bypass
subject-conflict checks. Primary user creation, identity-verification evidence,
profile editing and approval/access-review screens remain separate product
workflows. Existing primary accounts can use authenticated MFA
enrollment and module screens after explicit provisioning; this CLI supplies
neither a generic user-management exemption nor a complete onboarding UI.

Targeted checks use a disposable PostgreSQL URL and isolated schemas:

```sh
EXTENSION_ADMIN_TEST_DSN=postgres://synthetic-test-only go test ./cmd/extensions-admin
# In the add-on repository:
API_DATABASE_URL=postgres://synthetic-test-only go test -tags integration ./api -run 'TestAccess|TestSQLScope'
DIRECTORY_DATABASE_URL=postgres://synthetic-test-only go test -tags integration ./directory -run TestIdentityTx
```

The examples are placeholders; never point these tests at an application database.
