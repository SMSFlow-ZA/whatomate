# Optional isolated-host resource allowances

Ordinary Whatomate defaults remain unrestricted and multi-organization. `[plan]`
is trusted operator configuration: `enabled=false` creates no allowance tables or
guards; enabling sets user, WhatsApp-number and local-media limits across this
isolated database. `max_organizations=0` preserves multiple organizations, while
an explicit positive limit (such as 1) opts into that hosting restriction.

All nondeleted records count, including inactive users and unconnected
numbers. No reserved support account, billing/provisioning provider or deployment
identity is introduced. Lowering limits preserves existing rows and files; ordinary
updates, deletion and storage reductions remain possible. Concurrent additions
are fenced by PostgreSQL counter updates. Stable boundary errors return HTTP409
with `plan_limit_reached` and a resource name. Host controls have no HTTP setter. Authenticated superadmins can read aggregate
usage and the installed limits at GET /api/plan.

Uploads, campaign publication and IVR/TTS audio use the same local storage budget.
Temporary conversion stays outside persistent storage; failed publication restores
previous files. Symlinks and destinations outside configured roots are rejected.
Enabled allowances require local storage and disabled call recording because the
other storage backends are not counted by this package. API-only servers and
standalone workers install/check the same controls. A database already marked
for enforcement refuses disabled configuration on restart; deliberate removal
requires an operator migration while all application processes are stopped.

This package is discussed in #603. Maintainer discussion is required before a
substantial feature PR; no upstream acceptance or release is assumed.
