# Internal response-target notifications

Internal notifications help staff find at-risk or breached inbox response targets.
They are optional, disabled by default and delivered **inside the application**.
They do not send a customer reply, email, SMS or WhatsApp message, change an
assignment, or change the case's response policy, calendar or deadlines.

## Configure notifications

Open **Settings → Inbox workflows → Response targets**, choose a WhatsApp account,
then open **Internal notifications**. Select at-risk and/or breach, find the
intended staff members, explicitly enable notifications and Save. Selecting a
person never grants access to cases. Staff search follows directory permissions;
an unavailable saved name appears as a staff reference rather than an invented
identity. Remove or review an unavailable recipient before enabling.

Unsaved changes are protected when switching accounts or sections. Cancel restores
the saved settings. A failed staff lookup preserves the selection and offers Retry.
A stale revision or uncertain save stops further changes until **Refresh saved
settings** succeeds and the operator reviews the current configuration; the app
does not replay that save automatically.

**Observation history** below the editor shows the recorded threshold, target,
time and per-recipient outcome. Expand an observation for case/policy references,
the routing revision and suppression reasons. This is scoped operational history,
not proof that a human saw the notice.

## Review your notices

Open **Response alerts** from the Chat inbox toolbar or Live inbox. Ordinary agents
use their existing chat permission and case access; supervisor reporting permission
is not required. The list starts with unacknowledged notices. Clear that choice to
include acknowledged history, and use Older/Newer for additional pages.

Each notice identifies the account and case, the observed threshold and target,
local timestamps with timezone, the assigned immutable policy and current target
state. **Threshold due** is the observed crossing time (the risk threshold for an
at-risk notice), not a new reply deadline. Open the case to inspect its response
deadline and current work. Completed or resolved work is labelled separately from
the originally observed warning or breach.

**Acknowledge** changes only your internal notice. It does not send a reply, mark a
customer message read or resolve the case. A failed acknowledgement retains the
notice; refresh before retrying. Repeated acknowledgements are idempotent.

Visible lists refresh after focus/reconnection, on scoped live hints and periodically.
Older pages stay in place and show that new activity is available. Last updated and
Refresh make freshness explicit; failed refreshes do not masquerade as current
data. Current access is rechecked on every request. Missing access hides retained
rows; a disabled inbox feature is confirmed separately from an unavailable route.

## Activation and thresholds

Configure notification routing separately for each WhatsApp account. Select up to
20 active staff members with chat-read access, choose at-risk and/or breach, and
explicitly enable it. Inbox workflows must also be enabled for the organisation.
Setting response targets alone never enables notifications.

Each threshold records its own activation time. Only crossings on or after that
time are observed. Enabling or re-enabling a threshold does not backfill old
crossings. Editing recipients does not rearm an already observed crossing.
Disabling routing stops new observations; retained notices remain history.

The worker uses the assigned immutable policy's existing obligation projections:
first human reply, next human reply and resolution. Business calendars, date
exceptions and waiting-for-customer pauses keep their existing meanings. An
at-risk notice is not generated after the target is already breached. If the
worker was unavailable through both crossings, it observes the current breach
when eligible, rather than creating a stale warning as well. The exact deadline
is not itself late.

A recorded handoff can link a case to legacy transfer escalation. Reply-target
notices are suppressed for a recipient already covered by that active transfer's
escalation. Sharing a contact or another account is insufficient evidence. The
separate inbox resolution target remains eligible. Suppression is recorded, not
reported as successful delivery.

## Permissions and privacy

Configuration requires current general-settings read/write as appropriate, chat
read and full contact read. Browsing recipient names additionally requires user
directory read. Eligible recipients must belong to the organisation directly or
through an explicit membership. A global administrator's configuration rights do
not automatically make them a recipient in every organisation.

At observation, role and membership data are loaded fresh, without trusting a
Redis permission cache. Every recipient also needs current access to the exact
case/account. Denied or unavailable recipients receive no notice. Read, mark-read
and live-hint operations recheck current access; historical receipts cannot
restore a former assignment or removed membership.

Own-notice responses expose only scoped case/account identity, safe display names,
policy version/name, target kind, timing, read state and current target state.
Phone-like display names follow the organisation's masking policy. No message
body, custom-field values, credential or other recipient list is returned.

## Authenticated API

Use normal workspace authentication and CSRF protections. All lists have fixed
bounds; no arbitrary offset is supported.

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/api/inbox/sla/escalations/{account_id}` | Routing settings, revision and recipient limit. |
| PUT | `/api/inbox/sla/escalations/{account_id}` | Save complete routing settings at the current revision. |
| GET | `/api/inbox/sla/escalations/{account_id}/recipients` | Eligible staff, with authorised directory access. |
| GET | `/api/inbox/sla/escalations/{account_id}/history` | Scoped observation and recipient outcomes. |
| GET | `/api/inbox/sla/notices` | The current user's accessible retained notices. |
| PUT | `/api/inbox/sla/notices/{id}/read` | Idempotently acknowledge an accessible own notice. |

```json
{
  "revision": 0,
  "enabled": false,
  "at_risk": true,
  "breach": true,
  "recipient_ids": ["00000000-0000-4000-8000-000000000001"]
}
```

An account without a saved definition returns revision `0` and remains disabled.
Supply all three booleans on save. Enabling requires at least one threshold and
one eligible recipient. Duplicate, missing, foreign, inactive and over-limit
recipient selections are rejected. An authorised operator can still disable an
existing configuration after a recipient loses access. A stale revision returns
HTTP 409 with `inbox_escalation_conflict`; invalid settings use HTTP 400 and
`invalid_inbox_escalation`. Refresh and review before submitting another change.

Own notices accept optional `account_id`, `unread=true|false` and `cursor`.
Notice/history pages contain at most 50 items and return `has_more` and
`next_cursor`. Recipient lookup accepts literal name `search` (up to 100
characters) and its UUID `cursor`, with at most 100 candidate identities per page.
An eligible page can be empty while `has_more` still permits continuing the
directory search. No unscoped total is exposed.

`available` means the notice is durably retained in the app, not that a socket or
person received it. Other receipt outcomes include `recipient_unavailable`,
`case_access_denied` and `legacy_transfer_covered`. `target_state` describes the
current target (`active`, `breached`, `paused`, `completed` or `case_resolved`),
separately from the original observed threshold. Mark-read only acknowledges the
internal notice; it is neither a customer-message read receipt nor a reply.

## Durability and recovery

Each organisation/obligation/policy-version/threshold has one durable event, with
one receipt per selected recipient. Event and receipts commit together. Repeated
cycles, concurrent workers, restarts and later recipient changes do not create a
second item for that crossing. Storage failures roll back the entire observation
and can be retried. One failed observation does not stop other items in its batch.

After commit, `inbox_notice_available` is a best-effort WebSocket invalidation
containing only the notice ID, sent to the scoped recipient. It is not a delivery
receipt. Clients reload the scoped list after reconnecting; missed hints do not
erase durable notices. Reads, disabled settings, lost access and completed work
prevent a pending hint from being presented as new actionable work.

Back up settings, events and notices together with cases, immutable policy
versions, obligations and identities. Do not delete deduplication records to force
replay. Restore with the application stopped, disable notification routing in the
restored database before starting workers, review current work, then deliberately
rearm thresholds if appropriate. Older application images
ignore these additive tables; preserve them when rolling back. Combined managed
upgrade, restore and rollback acceptance remains a separate release gate.
