# Conditional response policies

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

## Configure in the application

Open **Settings → Inbox workflows → Response targets** and choose a WhatsApp
account. **Account default** keeps the existing target behaviour; **Conditional
policies** contains the ordered exceptions. Read-only users can inspect saved
definitions without receiving write controls.

1. Select **New conditional policy**, name it and describe its purpose. It
   inherits a copy of the account default's targets and calendar, when available.
2. Optionally restrict it to a team and add priority or custom-field conditions.
   A team restriction must match independently of the all/any condition group.
   An empty group without a team matches every new case; place such a policy
   after more specific policies.
3. Set durations in minutes. For business hours, choose an explicit timezone,
   weekly hours and any date exceptions. An exception replaces that local date's
   entire weekly schedule; leave its hours empty for a closed holiday. Exceptions
   do not repeat each year. Explicitly remove them before switching to elapsed
   time. Separate overnight hours at midnight using the existing weekly controls.
4. Save the disabled draft. Use **Preview policy selection**, choose an accessible
   case from this account, then **Preview this case**. The result shows every
   candidate's outcome, the prospective winner and the sample's unchanged assigned
   version. Preview evaluates saved definitions, not unsaved editor changes.
5. Edit the reviewed draft and choose **Enabled for new cases**, then save. Move
   policies with the up/down controls to set precedence. A clone starts disabled;
   archiving removes a candidate from future selection while retaining its history.

New cases use current settings. Existing cases retain the version assigned when
they entered the human queue, even when their priority, team or fields later change.
The expanded case response-target details display that version's policy name.

Leaving an edited form asks whether to keep or discard the draft. Stale revisions
retain the draft and require refreshing the saved list before another edit. If a
save's result cannot be confirmed, it may already have succeeded: submission is
held until the list is refreshed and reviewed. A confirmed save followed by a
failed list refresh is reported separately. Account, organisation or permission
changes invalidate results from the previous context.

If custom-field details cannot be loaded, the editor displays the recorded field
identifier and retains its comparison and value. Retry the lookup before changing
the value or saving the policy. Choosing another field or removing the condition
is an explicit edit; a failed lookup never silently substitutes a different field.

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

Optional [internal notifications](inbox-internal-notifications.md) use separate,
disabled-by-default routing under the Internal notifications tab. Staff can review
their scoped Response alerts from the inbox. Configuring a response policy alone
does not notify staff or activate customer replies.
