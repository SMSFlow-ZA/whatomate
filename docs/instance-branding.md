# Runtime instance branding

Whatomate name, logo, favicon and green accent remain the defaults. Administrators
with settings.branding:write can set the display name, normalized PNG/JPEG assets
and accent at Settings → Branding, or reset them without a frontend rebuild.
Organization names, WhatsApp profiles and ordinary multi-organization behavior
are unchanged. The public GET metadata/assets contain presentation data only.
PUT/DELETE use upstream authentication and explicit branding permissions.

Names are limited to 80 Unicode characters without controls; colours use six-digit
hex. Logo/favicon upload bounds are 2 MiB/512 KiB and 2048 pixels per dimension.
SVG is rejected. Images are normalized to PNG, versioned by SHA256 and confined
to the media tree. Durable journals plus database commit markers reconcile process
death, failed SQL and lost commit acknowledgements. All readers and writers share
the publication lock; startup recovers interrupted publication before serving.
Concurrent changes serialize; the last completed save wins.

`App.BrandingStorageLimit` optionally resolves a host budget while holding that
lock; nil leaves ordinary installations unlimited. No managed plan, support
bootstrap, billing provider, deployment identity or custom publishing workflow is
included. Semantic status colours retain their meanings and brand control text
uses computed contrast in both themes. Existing source/license notices remain.

This is the focused package discussed in #602. `CONTRIBUTING.md` requests discussion
before substantial feature PRs; the issue is not an upstream acceptance decision.
