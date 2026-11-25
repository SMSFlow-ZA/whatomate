# Inbox invalidation primitives

The database protocol carries resource identifiers and revisions. Authoritative
HTTP reads continue to enforce content and field permissions. A transport must
check current membership, permissions and its selected watch before delivering an
invalidation; `Event` is an internal database record, not a public response DTO.

`InstallRevisions` adds idempotent triggers. Case transitions/activity, deadline
projections, intake summaries and field changes stage identifier-only hints in
their writer's PostgreSQL transaction. Field values, notes and intake answers
remain in their original tables and are excluded from notifications.

The deferred flush coalesces each transaction's changes by resource, preserving
the first known previous scope and the final new hint. It acquires revision keys
in deterministic organisation/kind/resource order after business writes finish,
advances a counter and calls `pg_notify`. PostgreSQL delivers the notification
after successful commit. Rollback erases both staged changes and revisions.
The staging table is empty after commit; its rows are never restored send jobs.
Keep its constraint trigger deferred in application transactions.

Per-resource counters preserve ordering even when transactions start in one
order and commit in another. Global allocated sequence numbers cannot serve as
a reliable commit cursor. `UpdatedAt` is diagnostic; ordering uses the revision.
Compatible older SQL writers still invoke installed triggers. Installation does
not reconstruct events for existing records, and reinstalling preserves counters.

`Decode` accepts a bounded version1 payload and rejects unknown fields, invalid
identifiers, kinds and revisions. Log safe categories rather than raw rejected
payloads. Before/after identifiers are routing hints: a field deletion can produce
an empty after hint while its case still exists. Fetch current state and authority
instead of treating a hint as proof that a case was deleted or access was granted.

Notification loss, listener reconnect, a new connection/watch epoch, a revision
reset after restore or a transport gap must cause an authorised resynchronisation.
Use bounded caches and coalescing, retain fallback polling, and preserve active
drafts and selections. Notifications never authorise a message, workflow or
external integration action.

The PostgreSQL integration test exercises actual LISTEN/NOTIFY, rollback,
concurrency, no-op writes, old SQL writers, listener restart, field privacy,
transaction coalescing and interleaved case/deadline writes. It also verifies that
the staging table is empty after commit and internal models serialize without
routing data.

## Listener lifecycle

`Listen` owns one notification source and a128-update internal queue. Connection
success, notification loss, invalid payloads and overflow reset its epoch so a
consumer can resynchronise. A full queue discards obsolete hints and retains a
reset instead of blocking database reads. Retry delay is bounded; cancellation
closes the source and output. `PostgresConnector` copies an explicitly supplied
connection configuration and reports credential-safe categories.

An available connection does not prove a screen is current or authorised. The
application transport must validate each watch and delivery, detect sequence gaps,
refresh through scoped APIs and protect drafts/selections. Expose independently
constructed public frames; the internal `Update` serializes without its fields.

Fake-source tests cover retries, reset ordering, restored lower revisions,
overflow, invalid payloads and cancellation. The opt-in PostgreSQL test checks
real commit-only delivery, reconnect after terminating its own uniquely named
test connection and credential-redacted failures.
