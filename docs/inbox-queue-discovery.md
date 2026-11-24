# Queue ordering and date discovery

The queue-discovery API extends existing inbox filters without changing workflow
conditions. The backend foundation is implemented; the new sort/date controls and
their complete managed-release acceptance remain in progress. Existing queue UI
and legacy offset requests keep their current behaviour.

## Queue requests

`GET /api/inbox/queue` accepts an optional JSON `discovery` query parameter, beside
the existing `filters`, `view_id` and `include_counts` parameters:

```json
{
  "order": "latest_activity",
  "timezone": "Africa/Johannesburg",
  "opened_from": "2026-11-01T00:00:00+02:00",
  "opened_before": "2026-12-01T00:00:00+02:00"
}
```

| Order | Meaning |
| --- | --- |
| `priority_waiting` | Current urgent/high/normal/low priority, then oldest human-queue entry. |
| `latest_activity` | Latest case-scoped customer or human-reply activity, descending. |
| `longest_unanswered` | Oldest first/next human reply still outstanding; cases without one appear last. |
| `newest` | Newest case entry. |
| `oldest` | Oldest case entry. |
| `nearest_deadline` | Earliest verified current, incomplete and unpaused response obligation; absent deadlines appear last. |

Stable case identifiers break ties. Priority/waiting retains its existing ordering
exactly. Activity never uses contact-wide last-seen, unrelated account activity,
internal notes, status changes or delivery receipts. Human-reviewed macros follow
the existing human-reply path; automatic rule sends do not become human replies.
No WhatsApp session-window or SMS-delivery behaviour changes.

Optional range keys are `opened_from`, `opened_before`, `activity_from` and
`activity_before`. From is inclusive; Before is exclusive. Bounds are RFC3339
instants with at most microsecond precision, matching PostgreSQL precision.
A valid IANA `timezone` is required with a bound and retained for display/context.
It does not reinterpret an already specified instant. For an inclusive calendar
end date, clients must convert the next **local calendar day** to an exclusive
instant in the selected timezone; adding24 UTC hours fails across DST changes.
Empty bounds are allowed; equal/reversed ranges, unknown properties, unsupported
sorts, invalid zones and excess timestamp precision reject with HTTP400.

Current organisation/account/case access, mandatory shared-team scope, ordinary
filters and date ranges apply before sorting/pagination. Counts use the same date
and visibility rules. One response's rows, counts and response-target metadata are
read in a short read-only database snapshot.

## Paging and freshness

Requests including `discovery` or `cursor` use keyset paging: at most50 cases,
`has_more` and an opaque `next_cursor`. Send the returned cursor with the same
viewer, filters, sort/date preferences and view context for the next page. Do not
mix a nonzero offset with keyset paging. Invalid or context-mismatched cursors use
HTTP400 and `inbox_discovery_invalid`. Cursors never grant access; every page
reapplies the current mandatory scope.

Responses advertise `discovery_version: 1`, effective `discovery`, `paging`
(`keyset` or `offset`) and `updated_at`. Legacy requests without discovery/cursor
retain offset paging. A missing order resolves to `priority_waiting`.

A cursor is not a database snapshot spanning multiple requests. Each page checks
a freshness marker over the currently matching, authorised cases and their work
and ordering state. A changed set or case requires a first-page refresh: HTTP409,
code `inbox_queue_changed`, with no replacement cases in that response. Activity
on unrelated accounts or contact-wide last-seen timestamps is not included.
This detects changed matching work; it does not freeze conversations or replace
authorisation. Older clients without discovery retain their offset behaviour.

Keep older displayed pages in place, expose their age and offer an explicit
first-page refresh when this conflict or scoped updates/reconnection invalidate them.
The new queue UI and shared live invalidation transport are separate acceptance
work; this API foundation does not claim that those interactions are complete.

## Saved views and upgrade behaviour

Create/update view requests accept a separate `discovery` object. View lists return
that object and `discovery_version: 1`. Forward the selected view's discovery in
queue requests; the server does not silently apply hidden new preferences to a
legacy client. The view's mandatory shared-team restriction always applies, even
when a client supplies other filters or sort preferences.

Omitting discovery (or sending null) on an existing view update preserves its
recorded preferences. An explicit empty object resets to current default ordering
with no date range. Older views resolve to the same default. Existing ownership,
team publication and edit/remove/share capabilities remain unchanged.

Discovery is stored separately from workflow/rule/response-policy conditions in
an additive column. Previous images ignore it and retain legacy queue behaviour;
do not remove the column during rollback. Preserve it with saved views in backups.
Combined upgrade/restore/rollback and live release checks remain mandatory.

This foundation introduces no inferred campaign attribution. A future source
label must use an actual message/campaign relationship; a contact's unrelated
campaign membership is not evidence for the current case.
