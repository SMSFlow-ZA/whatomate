# Authentication and authorization

Protected API families require their resource/action permission on the server.
Custom roles receive only their configured permissions. Existing record-level
organization, assigned-contact and team checks also apply. Catalogue management
uses account permissions because the current permission model has no separate
catalogue resource. Unknown API families fail closed.
Campaign execution uses `campaigns:execute`; custom action execution uses the
existing `custom_actions:write` permission. Pickup-only agents can resume only
their own assigned transfers. Queue agents receive only the pickup feature flag
from the chatbot settings route; the full configuration and statistics require
`settings.chatbot:read`.

Access, refresh and WebSocket tokens have separate purposes and audiences and
require the `whatomate` issuer and HS256 signature. Refresh tokens require a
single-use JTI and a live Redis session. WebSocket tickets are single-use and
expire after 30 seconds. Session storage failures prevent authentication and
token issuance. Redis authentication/session keys must be retained for the
intended session lifetime; losing them requires users to sign in again.

Every API request and WebSocket authentication checks the current active user
and organization membership. Disabling a user or changing a password revokes
their sessions. Established WebSockets recheck account/session validity every
five seconds, with a five-second storage timeout. Refresh rotation replaces the
previous session, so existing sockets reconnect with a fresh ticket. Logging out
revokes the selected session, including its access token, while preserving other
independent sign-ins.
Rotation preserves the authenticated revocation epoch, and token issuance
rechecks its credential snapshot, so an in-flight request cannot adopt a later
password-reset epoch.

Browser logout uses `POST /api/auth/refresh/logout`, under the refresh cookie's
existing narrow path. The older `/api/auth/logout` endpoint remains available
for API clients supplying `refresh_token` in JSON. Browser clients must use the
new path. Logout storage failures return an error and the browser retains its
signed-in state so the user can retry.

## SSO identity linking

Email alone never links an external identity to an existing account. An existing
account must match its organization, provider, external subject and saved provider
configuration binding. New SSO users can still be created when an administrator
explicitly enables automatic creation. The reserved managed support identity
continues to use its host-controlled native authentication.

An administrator with `users:write` explicitly links or re-links an existing
native/SSO user using `PUT /api/users/{id}`:

```json
{
  "sso_config_id": "<enabled-provider-configuration-uuid>",
  "sso_subject": "<external-provider-subject>"
}
```

The provider must belong to the user's organization. A zero UUID in
`sso_config_id` removes the link. The provider configuration ID is returned as
`id` by the administrator SSO settings API. Linking revokes existing sessions. Provider identity
is pinned to its saved ID, organization, type, client credentials and endpoints;
changes to these values require explicit re-linking. An in-progress OAuth flow
also fails if its configuration changes.

## Upgrade compatibility

Run the normal database migrations to add the user SSO binding column. Existing
tokens without the required purpose/audience/session state are rejected; users
must sign in again. Legacy SSO links without a configuration binding require
explicit administrator re-linking. Native login remains available for existing
native accounts. Grant newly enforced resource permissions explicitly to custom
roles that need the affected API families.
