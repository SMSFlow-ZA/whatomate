# Inbox message personalisation

This extension is under development. It extends the opt-in inbox workflow; existing authored replies retain their behaviour until personalisation is explicitly selected.

## Author a reply

Use **Insert variable** beside a workflow reply or in the conversation composer. Search by label or key, choose a variable and insert it at the current selection. Inserting a variable selects **Personalise this reply**. Turning that option off preserves braces as literal text.

Available built-ins are `contact_name`, `phone_number`, `agent_name` (also `user_name` for compatibility), `account_name`, `case_id`, `case_status` and `case_priority`. Custom field keys use `contact.field.<key>` or `case.field.<key>`. The catalogue contains definitions, not customer values. Contact fields require contact-read access; phone masking also applies to variable resolution.

Agent name uses the user applying a macro or the rule's executing owner. Case status and priority use their stored codes, such as `waiting_customer` and `high`.

For an explicitly recognised but empty value, author literal fallback text:

```text
Hello {{contact_name | "customer"}}, your case is {{case_id}}.
```

Fallback text is a JSON string of at most 256 characters. The resolver is not a scripting language. It cannot read configuration, credentials, environment variables, other contacts or unrelated cases. Values are substituted once: braces inside a stored value never execute as another variable.

Unknown or inaccessible fields block sending even if a fallback was authored. An archived field is reported as archived and may use an explicitly authored fallback. Empty values, including empty strings, are distinct from valid number `0` and boolean `false`.

## Preview and confirm

Workflow **Preview** and rule dry runs display the intended recipient/account and rendered message, including specific missing-value problems. A blocked WhatsApp send still shows the authored intent. The composer opens **Review personalised reply** before sending; this requires a current, unresolved case.

Confirmation is tied to the actor, account, case/version, source and rendered values. Sending repeats access, case, variable and WhatsApp eligibility checks. Changed values require another preview. Direct composer retries retain the same client operation identifier; an uncertain result requires checking message history, not starting a new operation blindly.

Review confirmations are signed and become invalid after an application restart. Preview again to continue. Durable submission identifiers and queued-send reconciliation remain independent of that confirmation.

Previewing never sends a WhatsApp message. Personalisation does not enable a rule, automatic replies, SMS or fallback.

## Utility template parameters

Workflow utility templates may map text header/body parameters separately. For example, header parameter `1` and body parameter `1` can have different values. Each required parameter must be mapped; additional unknown mappings are rejected. Select personalisation to resolve permitted message variables inside a mapping, or leave it off for literal mapped text.

Templates must still be approved, belong to the selected WhatsApp account and satisfy the existing utility/consent requirements. Media headers, template buttons and footer variables remain unsupported by the workflow action. Unsupported content is rejected explicitly.

## API additions

- `GET /api/inbox/message-variables`: authenticated, inbox-enabled, permission-scoped definition catalogue; no subject values.
- `POST /api/inbox/message-preview`: authenticated, inbox-enabled preview for an accessible current case. Request `{ "case_id": "<case-uuid>", "text": "Hello {{contact_name}}" }`.
- Workflow actions add optional `personalise`, `body_parameters` and `header_parameters` properties. Existing JSON actions omit them and retain literal text.
- The existing contact message endpoint accepts optional `personalisation` with `case_id`, `version`, `authored_text` and preview `confirmation`. It requires a nonzero UUID `client_message_id`, matching rendered text, a current accessible case and chat-write permission. This form supports WhatsApp text; SMS and interactive payloads cannot bypass those checks.

Personalised messages are bounded to 4,096 Unicode code points and 64 variable occurrences. Header text is bounded to 60 code points; template mappings support at most 32 body parameters and one header parameter. No silent truncation occurs.

## Upgrade and recovery

Workflow parameters remain in the existing actions JSON; this part introduces no new persistence service. Preserve the instance database and encryption configuration using the ordinary recovery procedure. Restored outbound work must remain held until reconciled. Retain the previous image, but do not enable sending under an older image that does not understand newly authored personalisation settings.
