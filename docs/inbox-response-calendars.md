# Response policy calendars

Response targets use the calendar stored in the immutable policy version assigned
when a case enters the human queue. Editing the active account policy affects
future cases. Existing first reply, next reply and resolution deadlines continue
to use their assigned version, including any date exceptions.

The account policy API accepts an optional `date_exceptions` list alongside its
existing weekly `shifts`, timezone and target durations. Each exception names an
exact local date and replaces that date's complete weekly schedule. An empty
interval list closes the date. Exceptions do not repeat annually. A special
weekend opening is permitted; weekly hours apply again on dates without an
exception. At least one weekly interval remains required.

For example, include this field in a business-hours policy to close 25 December
and open 26 December from 09:00 to 12:00 in the policy's IANA timezone:

```json
{
  "date_exceptions": [
    { "date": "2026-12-25", "intervals": [] },
    {
      "date": "2026-12-26",
      "intervals": [{ "start_minute": 540, "end_minute": 720 }]
    }
  ]
}
```

Dates must be unique and use `YYYY-MM-DD`. The maximum is 366 dates, each with
at most 28 non-overlapping intervals. Minutes range from 0 through 1440, and each
interval must have a positive duration within one date. Split overnight hours
between the two dates. Adjacent intervals are allowed. Invalid input is rejected
before a policy version or active policy is written. Exceptions require
business-hours mode and a valid recurring schedule and timezone.

Clients that omit `date_exceptions` preserve the active calendar while updating
other targets. Send an explicit empty list to remove all exceptions. Switching
to elapsed-time mode requires removing exceptions explicitly. Policy updates
and this preservation happen under the same account policy lock used by case
selection. Existing clients with weekly-only policies keep their original
behaviour.

Displayed targets, persisted obligations, completed-cycle calculations and the
**Next working day** snooze suggestion use the same calendar. Daylight-saving
gaps contribute no working time; repeated working wall times contribute their
actual elapsed duration. The suggestion starts from the next local date and
does not save a snooze until the agent confirms it. Waiting for the customer
continues to pause only the resolution clock.

Calendar exceptions add data that older weekly-only application binaries cannot
interpret correctly. An additive database column is not sufficient proof of
rollback compatibility. Preserve a compatible image and a verified recovery
point, and keep outbound work held while performing the release's tested paired
restore and reconciliation procedure. Do not resume exception-enabled cases on
a binary that ignores their calendar.

Calendars are part of the immutable versions used by
[conditional response policies](inbox-response-policies.md). Configure named
policies and optional internal escalation in the response-policy editor.
Saving calendar targets does not enable a workflow rule, customer reply or
internal notification; escalation has its own explicit activation controls.
