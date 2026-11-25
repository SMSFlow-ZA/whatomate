# Guided intake and conditional case requirements

Inbox workflows must be enabled for the organisation. Existing fields, cases,
message history and bot steps retain their behaviour until an administrator
explicitly configures a requirement or selects an intake mapping. Creating a
mapping saves it disabled; it does not enable a bot or send a message.

## Resolution requirements

In Settings → Custom fields, choose Optional, Always required, or Required when.
A conditional requirement can restrict a WhatsApp account and team, then match
up to twenty typed priority/field conditions using All or Any. Account and team
restrictions always apply outside that group. Conditions inspect current case
and contact values, not unrelated message history.

Empty/null/whitespace differ from No and zero. Archived definitions retain their
recorded values but are no longer required. Existing always-required fields stay
unconditional. A definition revision prevents a stale editor from overwriting
another administrator's changes; reload deliberately to discard a stale draft.

All resolution paths use the same transactional guard, including the API, bulk
actions, macros and rules. A simultaneous priority change is evaluated using its
proposed priority. The completion form saves permitted values and resolves
atomically. Missing contact-edit permission offers a supervisor handoff rather
than granting that permission.

Email address and HTTPS link are explicit new field types. Existing Text fields
are not converted or reinterpreted. Email values contain one address without a
display name or surrounding spaces. Links require HTTPS and no embedded user
credentials. Validation never fetches a link.

## Reusable intake mappings

Open the Guided intake tab in Settings → Custom fields. Administrators need
General settings Write, Chatbot flows Write, Chat Write, and Contacts Read/Write.
Those rights are checked again from the database when collecting and applying
answers; a stored owner or stale permission cache does not grant access.

Give the mapping a name, an explicit consent-answer key and 1–20 unique
input/field pairs. Choose active contact or case definitions. Input names use
lowercase letters, digits and underscores, start with a letter and have at most
64 characters. `flow_token` is reserved. Mark answers that should be flagged if
missing; this is independent of required-at-resolution rules.

Save, review, then explicitly enable the mapping. Select it on a Prompt, reply
Buttons, or WhatsApp Flow step. Form inputs use their authored answer names.
Prompt/buttons expose one `answer`; buttons use the configured reply identifier.
For prompt/button intake the customer must quote the original message, and the
session must already contain explicit boolean consent under the consent key.
For a form, include a boolean consent answer. Missing/false/textual consent does
not authorise field storage. Merely configuring a consent key is not consent.

Use a later human-transfer step to finish intake. SMS transport, SMS fallback,
URL cards and phone cards are not intake response surfaces. Existing transport
settings are preserved; choose an ordinary WhatsApp reply prompt before adding
a mapping. A mapping does not publish or modify a Meta form.

Numbers accept exact JSON numeric syntax, including zero. A textual numeric
reply can be parsed as that number; locale separators, leading plus signs,
ambiguous leading zeros and non-finite values are rejected. Yes/no field replies
accept booleans or exact `true`/`false`; consent itself requires a boolean.
Empty answers never clear existing values. No silent truncation occurs.

## Conflicts, privacy and handoff

Before dispatch, the server binds a random response identifier to the exact
organisation, account, contact, chatbot session/node, mapping revision and field
versions. A form must return that opaque token; a quoted prompt must match the
bound outbound message in the same account and contact. There is no public
intake callback or client-selectable target definition in an inbound reply.

Answers are validated and encrypted while awaiting the human queue, for at most
24 hours. The existing inbox maintenance worker erases expired staging. Intake
does not create a case merely to retain answers. On explicit handoff, the normal
case transaction rechecks permissions, consent, mapping/flow revisions, case
identity and durable field mutation counters. A newer human edit, even an edit
followed by clearing, is preserved. Ordinary recorded inbound increments may
advance the same case; human/automation changes do not masquerade as those
increments. Duplicate callbacks or repeated handoff processing do not write a
field a second time. Staging is erased after application or rejection.

Conversation details include a Guided intake handoff summary with saved,
omitted, invalid, missing and conflicting outcomes. Raw staged answers and
encryption material are not exposed. Agents without Contacts Read do not see
contact-field result labels. Case/account/current-assignment boundaries apply
to summaries, including historical case requests. Review the saved values in
the normal field panels and hand off uneditable requirements to a supervisor.

Mappings with archived targets show setup warnings. The separate Disable action
remains available even if a target has been removed, and invalidates unfinished
captures without changing saved fields or bot settings. Editing creates a new
revision; older unfinished captures cannot use the replacement configuration.

## API and recovery

- `GET/POST /api/inbox/intake-mappings`
- `PUT /api/inbox/intake-mappings/{mapping_id}` (current `revision` required)
- `POST /api/inbox/intake-mappings/{mapping_id}/disable` (`revision` required)
- `GET /api/inbox/conversations/{account_id}/{contact_id}/intake?case_id=…`

Configuration responses are bounded to 200 mappings and report truncation.
Case summaries expose the latest 50 receipts and report older results. Neither
interface returns answers or grants permission to read/edit another resource.

Include intake mappings, bindings, field mutation stamps and the instance
encryption key in existing recovery procedures. The additive mutation trigger
covers prior application writers after rollback; stamps survive clear/recreate
and roll back with failed transactions. Older backups without these additions
migrate additively and have no fabricated historical intake. Verify decryption
and receipt idempotence in the isolated recovery rehearsal. Keep restored
outbound work held until its existing reconciliation checks finish.
