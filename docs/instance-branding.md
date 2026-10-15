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

## Managed release

Based on managed release `597ce32`, without unrelated upstream upgrades.
Application source: `50bb081d35ac40517c50a7788ab1ac4eca13c0b2`.
Pinned image:
`ghcr.io/smsflow-za/whatomate@sha256:8514b40328e51e540c8778b485ffa7aedf4d6506cc74ee92574d4655255f8508`.
Public review: https://github.com/SMSFlow-ZA/whatomate/pull/2.

Validation on 14–15 October 2026:
- 1,898 Go tests, 25 frontend tests, Vue type checking and production build passed.
- Independent review covered crash recovery, deferred cleanup, readable outline
  and navigation colours, editor loading failures and pinned ORM session isolation.
- Disposable managed-instance acceptance covered save, replace, remove, reset,
  upload validation, quotas, concurrent changes, administrator/support access,
  denied agent/anonymous edits and public metadata isolation.
- Branded login, desktop and 390-pixel mobile layouts, both themes, collapsed
  navigation, browser title and favicon were checked in a real browser.
- Custom branding survived application recreation, encrypted backup restoration
  and a genuine sandbox trial-to-paid upgrade of the same workspace.
- All four internal business instances received the pinned release with their
  existing administrators, limits and domains preserved. The disposable workspace
  and its sandbox recurring registration were removed after acceptance.

The media lock uses a fresh GORM statement session on the same pinned SQL
connection. Durable preimage journals and SQL commit markers recover interrupted
publication without leaking earlier quota/lookup statement state into the save.
