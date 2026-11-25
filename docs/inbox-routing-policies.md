# Named inbox routing policies

Routing policies are reusable settings under **Settings → Teams → a team →
Inbox capacity**. Enable inbox workflows for the organisation first. Team
administrators can create, edit and archive policies. A policy is a template:
saving or editing it does not change any deployed limits or send messages.

## Review and apply

Choose the WhatsApp account, policy and affected current team members. A policy
can inherit the team's strategy, use manual assignment, round robin or load
balancing. This strategy override applies only to that team/account inbox; it
does not change the team's existing chatbot transfer strategy.

Optionally copy an active-case maximum. Leaving this option off preserves each
agent's current maximum; zero means Unlimited. A separate assignment-rate limit
controls how many new ownership changes an agent can receive in a rolling
window of 1–1440 minutes. Turning it off does not erase recorded assignments.
Both limits are per agent and WhatsApp account, across teams.

**Review changes** shows the exact team, account, affected agents, effective
strategy, current values and replacement values. Nothing is reserved or changed
by preview. Confirm within five minutes. Changes to the source policy, membership,
scope or deployed limits require a fresh review. An uncertain Apply result offers
a retry of that same confirmation; it does not silently create a new request.
After completion, reload the current limits before making another change.

The applied copy retains its source name and revision. Editing or archiving the
template does not alter that copy. Explicitly review and apply a replacement to
change it. Applying a policy never enables workflow rules or automatic replies.

## Assignment and overflow

Open and Pending cases consume active capacity. A committed ownership change
consumes a rate slot; assigning the same owner again does not. Rolling back an
assignment rolls back its receipt. Reassigning away and back is a new assignment.
Restarts, repeated worker claims and rate-limit edits do not reset activity.
Assignments made by older compatible application versions also produce receipts
after this additive migration; earlier history is not reconstructed.

Named automatic routing chooses an available, active organisation/team member
with current Chat read and write permission, available capacity and a rate slot.
Load balancing compares active work in the selected account. Round robin uses
the existing team assignment order. Within a named inbox, higher priority and
then longest-waiting cases receive the next opportunity. If no eligible agent is
available, the case stays visibly unassigned with a reason. Existing workers retry
when a slot or availability returns; manual policies require manual assignment.

Workload and assignment controls show applicable limits and recovery times.
People without permission to inspect another agent's workload see a hidden-
activity explanation instead of a misleading zero. Availability affects automatic
routing; existing manual assignment behaviour remains available.

An authorised supervisor can override an active-case or assignment-rate limit
with a recorded reason. This does not bypass team membership or Chat permissions;
the actual override counts toward subsequent rate checks. Fresh team-write
permission is required even if a stale client still displays the override control.

## Macros, recovery and compatibility

Assign and Assign team actions use the same guards. Dry runs make no reservations.
Predictions account for earlier direct ownership changes; team routing after
earlier assignments is marked for rechecking because its fairness order can
change. Execution rechecks all limits under the account lock and rolls back
failed action sequences. No preview is a promise that an agent remains available.

The migration is additive and does not apply a policy or modify existing limits.
Backups must include policies, applied copies, per-agent limits and assignment
receipts together. Keep outbound automation held while restoring and reconciling
pending work. Older binaries retain their older routing behaviour; retaining
receipts is not a claim that an older binary enforces the new rate guard.
