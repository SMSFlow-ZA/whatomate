# Inbox interface conventions

Inbox features use the application's existing font, theme tokens, shared buttons,
popovers and dialogs. They do not introduce a second design system. Shared layout
and type roles live in `frontend/src/assets/inbox.css`.

## Reading order and density

- The queue starts with search, queue shortcuts and a compact action row. Sort
  remains directly available; filters and saved views open on demand. Bulk
  selection retains its full accessible name even when the visible label is short.
- Each case keeps contact and account identity, message preview, ownership,
  waiting age, status, priority and the most relevant response target together.
  Exact inbound timestamps remain available as row descriptions. Legacy target
  provenance is inside the expandable target details rather than repeated above
  every row.
- The case toolbar groups status, priority and Resolve/Reopen. Deadline details,
  activity and field editing remain separate from the message thread.
- A healthy update connection can use a compact, named indicator. Recovering,
  denied and pending-update states retain visible explanatory text. Connection
  status is not a guarantee that every record is current.
- Field controls use the full available panel width. Save/Cancel appear for dirty
  values; completion restores focus to the field. Required/archived annotations
  are stated once and remain attached to the field's accessible label.

## Type and interaction roles

The application's existing root size is 14px. Inbox body and compact section
titles use 1rem; labels, metadata and ordinary form controls use 0.875rem. Status
badges retain the shared dense size. Use weight and grouping for hierarchy instead
of arbitrary font sizes. Preserve user zoom. Mobile variable search uses 16px to
avoid automatic input zoom.

The variable picker has bounded width/height and a scrollable, themed listbox.
Search filters labels and keys; Arrow keys, Home and End select a visible option;
Enter/Space or the Insert button confirms it. Escape closes without inserting.
Insertion returns focus to the author's original input. Results, loading, empty
and retry states share the same type roles.

Use existing semantic colours for breaches, warnings and success. Responsive and
coarse-pointer controls retain 44px targets. Check both themes, narrow/short
viewports, long labels, keyboard navigation and the actual computed contrast.

## Release verification

Run frontend type/unit/build checks and the disposable Chromium inbox/audit suite.
Capture populated queues, field editing, the open variable picker, workflow and
routing settings in both themes at desktop and mobile widths. Verify label and
control sizing against neighbouring platform screens. These presentation changes
must not change permissions, rules, automatic replies or channel dispatch.
