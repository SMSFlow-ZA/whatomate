# Conditional response policy API

Conditional policies select response targets when a case first enters the human
queue. They do not route or reassign work, enable workflow rules, send customer
messages, change SMS dispatch, or enable internal notifications. The inbox
workflow remains an organisation opt-in.

The existing per-account response policy remains the default. An account may
have at most 20 ordered conditional policies ahead of that default. The first
enabled matching policy wins. If none matches, the account default applies; if
there is no default, the case has no response policy. An explicit no-policy
assignment is retained when policies are configured later.

Every new conditional policy and clone starts disabled, even if the create
request asks to enable it. Review and enable it with a subsequent versioned
update. Only future cases use that change. Updates create a new immutable
version containing the name, description, conditions, durations and calendar.
Existing cases retain their original version and its name after editing,
disabling, reordering or archiving the active definition. Target clocks, waiting
pauses and completed reply-cycle behaviour are unchanged.

## Configuration permissions

Configuration requires `settings.general` read or write, as appropriate, plus
the existing `chat` read and full `contacts` read permissions. This account-wide
configuration does not expand an ordinary agent's case or field access. Every
operation scopes account, policy, version, team and field identifiers to the
authenticated organisation. Sample-case previews additionally use normal case
read scope and the selected account.

## Routes

All routes are authenticated and use the normal session/API authentication and
CSRF protections. Replace identifiers with the selected account and policy.

| Method | Route | Behaviour |
| --- | --- | --- |
| GET | `/api/inbox/response-policies/{account_id}` | Ordered definitions, target snapshots, revisions and maximum count. |
| POST | `/api/inbox/response-policies/{account_id}` | Create a disabled conditional policy. |
| PUT | `/api/inbox/response-policies/{account_id}/{id}` | Save a complete definition with its current `revision`. |
| POST | `/api/inbox/response-policies/{account_id}/{id}/clone` | Clone the current `revision` into a new disabled definition/version. |
| PUT | `/api/inbox/response-policies/{account_id}/order` | Reorder the complete active list using IDs and revisions. |
| DELETE | `/api/inbox/response-policies/{account_id}/{id}` | Archive the current `revision`; retain historical versions. |
| POST | `/api/inbox/response-policies/{account_id}/preview` | Evaluate a `case_id` as a sample for future selection, without writes. |

Create/update accepts `name`, `description`, `conditions` and `targets`. Names
are at most 100 characters and descriptions at most 1000. Update also requires
the current `revision` and an explicit `enabled` value. Targets use the existing
account-policy format, including [date exceptions](inbox-response-calendars.md).
Account and version identifiers in submitted targets cannot select a different
account or existing version; the server sets them.

```json
{
  "name": "Priority enquiries",
  "description": "Shorter reply targets for high-priority enquiries",
  "enabled": false,
  "conditions": {
    "match": "all",
    "predicates": [
      { "field": "priority", "operator": "eq", "value": "high" }
    ]
  },
  "targets": {
    "first_reply_minutes": 15,
    "next_reply_minutes": 30,
    "resolution_minutes": 480,
    "business_hours": false,
    "timezone": "UTC",
    "shifts": [],
    "date_exceptions": []
  }
}
```

## Matching and preview

Conditions support priority and validated contact/case custom fields, using the
existing typed condition operators. The flat group has `all` or `any` matching
and at most 20 predicates, bounded to 64 KiB. No regular expressions, nested
expressions or arbitrary SQL are accepted. Empty values, false and numeric zero
retain their existing meanings. Archived field definitions retain recorded
matching behaviour.

An optional `team_id` is an additional restriction outside the all/any group.
The team must belong to the organisation and be active. An `any` condition can
never bypass team, account or organisation scope. Runtime matching uses stored
case facts, not a supplied team's pointer or a different account's case values.
Candidate queries are batched; queue entry does not load every candidate's full
calendar or perform one database query per condition.
The ordered candidate index excludes archived definitions; retained history
does not expand the active selection list.

Preview uses a read-only, repeatable-read transaction. It returns ordered
evaluations, safe per-condition match outcomes, and these reasons:

- `disabled`: the draft is not eligible, even if its conditions match.
- `conditions_not_matched`: the team restriction or condition group did not match.
- `first_matching_policy`: this is the prospective selected policy.
- `earlier_policy_selected`: it matches, but an earlier policy takes precedence.

Customer field values are not copied into preview outcomes. `prospective_only`
is true, and `existing_policy_version` identifies the sample case's retained
assignment. Preview neither changes that assignment nor creates a case, policy
version, obligation, activity or notification. Actual entry rechecks current
configuration and data under the account policy lock.

## Revisions and history

Reorder submits the complete non-archived list, including disabled drafts:

```json
{
  "order": [
    { "id": "00000000-0000-4000-8000-000000000001", "revision": 2 },
    { "id": "00000000-0000-4000-8000-000000000002", "revision": 1 }
  ]
}
```

Clone and archive submit `{ "revision": 2 }`. Changes affecting order advance
the affected definition's revision. A stale edit/reorder returns HTTP 409 with
`response_policy_conflict`; refresh before reviewing another change. Invalid
definitions use HTTP 400 and `invalid_response_policy`, and the count limit uses
`response_policy_limit`. Rejected multi-row reorders roll back every move and
revision change. A copied, missing or foreign identifier does not grant access.

Case `sla_selection` records the original decision and position without copying
condition values. `sla.policy_name` comes from the assigned immutable version.
Legacy backfill uses only the default known at migration, labelled legacy; it
does not apply newly authored conditions to historical work. Older cases lacking
selection metadata do not gain an invented historical match explanation.

Backups and recovery must preserve candidate rows, immutable versions and case
selection metadata together. Existing calendar rollback restrictions still
apply. Reconcile and hold outbound work during the release's tested restore and
rollback procedure.

This branch currently provides the API/selection backend. Its configuration
editor, calendar editor and optional internal escalation are separate work in
progress; this document does not claim those screens or notifications exist.
