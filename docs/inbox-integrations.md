# Inbox integration contract

This extension is under development. This checkpoint contains approved endpoint configuration, definition APIs, durable execution primitives and atomic result application. It is not yet connected to macro/rule execution or the settings UI. Reconciliation and safe retry controls remain to be implemented. Do not treat these APIs as an accepted customer release or enable live integrations from this checkpoint.

## Approval and access

An operator registers exact HTTPS endpoints in trusted `inbox_automation.integration_endpoints` configuration. Workspace administrators choose a registered endpoint key when defining an integration; agents cannot enter a destination, custom headers or executable code. Each endpoint fixes its method (`POST`, `PUT` or `PATCH`) and may declare a provider-supported `Idempotency-Key` or `X-Idempotency-Key` header. A declaration must be backed by the provider's durable idempotency contract. The header does not enable automatic retries.

Set an endpoint's `organization_id` to restrict its visibility and use to that organisation. Leaving it empty explicitly makes the endpoint available across organisations on that instance; use this only for intentionally shared destinations. Previews and administrative responses display the destination origin, not its path, because webhook paths can contain secret tokens. The full URL remains operator-side.

Definitions start disabled. Host `integration_dispatch_enabled` and the workspace integration switch default off and are separate from WhatsApp automatic replies and SMS dispatch. Disabling dispatch holds pending work. A request already in flight may finish; a stop cannot retract an accepted external operation.

Managing definitions requires general-settings write permission. Preparing/executing an action also requires chat write, custom-action write, active membership and access to the exact case/account. Mapping an output to a contact field requires contact read and write permissions. Macro visibility will not grant additional execution rights.

## Mappings

Definitions allow up to 32 named inputs of type text, number, boolean or date and up to 16 named output-to-field mappings. Inputs use the same bounded variable syntax and permitted contact/case resolver as message personalisation. Number `0` and boolean `false` remain values; an absent optional input is omitted. Unknown mappings, invalid types, unresolved variables and missing required inputs reject preparation.

Outputs select top-level response properties and validate them against active field definitions in the same organisation. No response body becomes script, a URL to fetch or an unrestricted update command. An optional reference key selects a bounded string reference. Only selected validated results are retained, encrypted. Result application checks current case identity, version, workflow/definition revision and field permissions before writing the entire field batch atomically. Empty outputs cannot silently clear existing values. A newer edit leaves the successful provider receipt held with `output_context_changed`; no second external request is made. Integration activity does not trigger rules.

## Transport and persistence

The client accepts exact HTTPS destinations without user-info, query strings, fragments or nonstandard ports. It rejects redirects, private/loopback/link-local addresses, metadata services, reserved address ranges and mixed DNS results. It dials the validated IP while retaining normal TLS hostname verification. Environment proxy settings cannot override this path.

Calls have a 30-second deadline, a 64 KiB request/response bound and a 16 KiB response-header bound. Responses must be a bounded JSON object with unique property names, or an empty HTTP 204 response. Errors expose categories and status/latency, never raw provider error bodies or credentials. The transport does not transparently replay requests.

Credentials are write-only and encrypted with the instance key. Secure storage fails closed if the key is missing. Request snapshots and selected results are also encrypted and never placed in Redis. Preserve the database and instance key together during recovery.

Each run/action index has one stable execution ID and immutable request digest. Atomic claims permit at most two integration submissions across workers in one instance database. Calls run after the claim transaction has committed, without holding the case lock. Completed work is never submitted again. A lost response or interrupted submission becomes `unknown` with `needs_reconciliation`; a non-success HTTP response is not proof that no external write occurred. Definitions, workflow visibility, actor permissions and case context are rechecked before dispatch.

## Administrative API foundation

- `GET/POST /api/inbox/integrations` and `PUT/DELETE /api/inbox/integrations/{integration_id}` manage definitions. Requests specify `endpoint_key`, not a URL. Updating without a credential preserves the existing encrypted value. Responses expose `has_credential`, never the credential or ciphertext.
- `GET /api/inbox/integration-catalogue` exposes scoped definition metadata/input schemas for the future action editor.
- `GET/PUT /api/inbox/integration-settings` read or change the workspace hold without changing unrelated organisation settings.
- Deletion rejects definitions still referenced by workflows or unresolved executions; use disable to stop future dispatch.

Execution/preview endpoints, reconciliation controls, ordered workflow processing and user-interface acceptance remain outstanding. Restore/rollback must keep integration dispatch off until pending and unknown work has been reconciled.
