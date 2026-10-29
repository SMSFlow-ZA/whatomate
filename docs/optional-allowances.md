# Optional instance entity allowances

Ordinary Whatomate defaults remain unrestricted and multi-organization. `[plan]`
is trusted operator configuration: `enabled=false` creates no allowance table or
guards. Enabling sets user and WhatsApp-number limits across this isolated
database. `max_organizations=0` preserves multiple organizations, while an explicit
positive limit (such as 1) opts into that hosting restriction.

All nondeleted records count, including inactive users and unconnected numbers.
Existing administrator behavior is preserved and every account counts toward
its instance allowance. There is no support-account bootstrap or reserved-user
exception. Lowering limits preserves existing rows and permits ordinary updates,
deletion and other reductions. PostgreSQL counters fence concurrent additions
and restores; a rejected transaction cannot leave a partial creation behind.

Stable boundary errors return HTTP409 with `plan_limit_reached` and a resource
name. Authenticated superadmins can read installed limits and aggregate usage at
GET /api/plan. Limits and counts are read in one SQL snapshot; no HTTP endpoint
changes operator allowances. API servers and standalone workers install/check
the same controls. A database already marked for enforcement refuses disabled
configuration on restart; deliberate removal requires an operator migration
while application processes are stopped.

This focused first contribution in the #603 discussion covers entity allowances
only. Media/filesystem publication and storage budgets are a separate held
contribution; this package adds no storage-backend or call-recording restrictions.
It contains no billing/provisioning integration or deployment workflow.
Maintainer discussion remains required before a substantial feature PR.
