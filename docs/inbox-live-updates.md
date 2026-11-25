# Live inbox updates

When inbox workflows are enabled, visible queues, conversations, field panels,
workload and response-target views can receive updates through the existing
authenticated WebSocket. No additional service or customer credential is needed.
Ordinary message delivery continues through its existing path.

**Live updates** means the update connection is available. It does not replace
the last-checked time or prove every displayed record is current. **Connecting
updates** means the connection is recovering. **Automatic refresh** means the
surface is using its independent polling/manual-refresh path. **Changes waiting**
means a refresh is held until an edit, review or selection is safe to preserve.
**Access changed** means the scope must be read again with current permissions.

Updates never send a reply or execute a workflow. Existing rules, automatic
replies, channel dispatch and integration controls are unchanged.

## Working without interruptions

- Queues retain staged refresh and pagination checks. A changed result set asks
  for a refresh rather than silently moving selected cases or mixing pages.
- Bulk selections, review dialogs and authored fields hold background refreshes.
  A form opened while a request is running is checked again before applying its
  response. Unsaved field values remain intact.
- A failed background field read keeps the loaded form and its current values
  visible. Retry stages the read while edits are active; Save or Cancel explicitly
  finishes those edits before waiting values can apply.
- Conversation history stays on the selected case, with its previous activity
  pages retained. A refresh does not switch a historical case to the current one.
- Hidden tabs stop watching. Returning renews access and reloads through the
  existing scoped interfaces. Polling and manual refresh remain available.
- Permission loss clears the affected private view even if an edit is active.
  Notifications never grant access to the new owner, account or team.
  Ordinary result changes retain staged refresh; a lost read grant instead
  clears stale rows and selections before the authorised reload.
- A temporary authority or session-storage outage is different from confirmed
  permission loss. It publishes no resource hints, retains drafts and uses the
  polling/manual-refresh path. Recovery verifies access again before resuming.

Aggregate capacity includes an agent's work across their teams on the selected
account. A capacity refresh grants no access to those individual conversations.

## Protocol and recovery

The socket accepts an `inbox_watch` message with protocol `v: 1`, a positive
`generation` and up to three deduplicated scopes. The ordinary 512-byte incoming
frame limit remains unchanged. Supported contexts are queue, case, conversation,
contact, capacity, workload and supervisor. Scope fields identify only the
client's selected context; organisation, identity and permissions come from the
authenticated socket. Empty scopes unsubscribe. Watches are rate limited and
expire unless renewed.

`inbox_state` responses contain the matching generation, epoch, sequence,
availability and scoped refresh hints. Public hints contain no message bodies,
phone numbers, field values, credentials or newly discovered assignment IDs.
Resource keys are connection-private opaque keys. Both subscription and delivery
check current database authority and applicable session revocation.
Existing permission caches for that viewer are reconciled against the fresh
authority before the reload. Cached grants never authorise an update.

Duplicate or older resource revisions are ignored within an epoch. A sequence
gap, changed epoch, reconnect or bounded-cache overflow requires an authorised
reload. Lower revisions after backup restoration are accepted in a new epoch;
completed send jobs are not recreated or dispatched by this mechanism.

PostgreSQL revisions and notifications commit atomically with the relevant write.
Rolled-back and no-op writes publish nothing. Schema additions are additive, and
installed triggers also cover compatible older SQL writers. HTTP-server shutdown
cancels and closes its single listener. Database/listener failures use safe log
categories without raw payloads or connection strings. See
[managed release and recovery checks](managed-plans.md#inbox-release-and-recovery-checks)
before deploying or restoring a modified build.
