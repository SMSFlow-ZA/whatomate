# Instance branding

Approved implementation: a Settings → Branding editor for the instance display
name, PNG/JPEG logo/favicon and primary accent colour. Managed defaults use the
SMSFlow public logo and cyan/navy palette. All plans and trials include branding.
Only settings.branding:write administrators and superadmins may save/reset.

Public GET /api/branding exposes display metadata only. Authenticated PUT accepts
multipart settings and optional images; DELETE resets overrides. PNG normalization,
2 MiB/512 KiB upload limits, 2048-pixel dimension limits, 80-character names and
six-digit colours apply. Public asset reads are confined to normalized branding
images. Storage writes must serialize both images and the database publication,
respect the existing media budget and restore previous files on a failed commit.

Runtime theme tokens, shared brand components and cache-busted asset URLs cover
login, desktop/mobile headers, browser title/favicon and interactive highlights.
Status colours, organization/Meta identities, source notices and billing are separate.

Execution ledger (14 October 2026):
- Base 597ce32; feature branch codex/instance-branding; implementation runs inline.
- Ruling: reuse the clean dedicated checkout rather than duplicate its files;
  build/test outputs go to C or disposable Docker volumes because F is nearly full.
- Task 1 implemented: singleton branding API and durable multi-file publication;
  SQL commit markers and preimage journals recover interrupted replacements.
  Tests cover validation, permissions, quota, process death and committed outcomes.
- Task 2 implemented: shared runtime theme/store/editor and accessible palettes.
- Task 3 reviewed: crash recovery, deferred cleanup, outline contrast and
  load-error protections added following independent release review.
- Corresponding source and immutable image publication pending.
- Task 4 pending: disposable acceptance, four-instance rollout, backup/restore,
  final fixture cleanup and runtime acceptance.
