# Protected handler notifications

The optional `notifications` module projects committed case escalation events
into content-minimal WhatsApp notices. Enable it explicitly with `conversation`
and `cases`, and configure exact `extensions.handler_notification_units`.
It requires the protected carrier, current directory, and host access store.

Activation records the database time and immutable account/unit scope. It does
not backfill earlier cases. Each current assignment is checked against the
active host user, current role and unit grants, all conflict aliases, and one
eligible directory identity. The same authority is checked again at dispatch.
Background checks do not create a user session or claim MFA verification.

The notice contains no case narrative. The recipient opens the protected
workspace to read the case. A signed current WhatsApp conversation window is
required; this module does not supply a template or SMS fallback. Delivery is
not evidence of human acknowledgement. Unknown transport outcomes remain held.

Install the three `addon_notifications_*` tables through the privileged schema
release owner before starting the new compiled runtime. Runtime DDL remains
disabled. Case-derived notice and receipt records participate in existing
privacy holds, governed erasure, and restore reconciliation. Disabling the
module stops its projector without deleting historical records.

Tests cover current permission revocation, historical identity conflicts,
transient database failure, duplicate projection, purpose isolation, concurrent
commit ordering, privacy holds, and restored-tombstone dispatch refusal.
