# Account-bound WhatsApp webhook subscriptions

Meta supports a WABA-level alternate callback for supported webhook fields. The
subscription request is `POST /{api_version}/{business_id}/subscribed_apps` with
JSON `override_callback_uri` and `verify_token`. The configured account's API
version and access token are used. This does not update the shared app callback.
See Meta's [webhook override documentation](https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/override/)
and [Subscribed Apps API](https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/whatsapp-business-account/subscribed-apps-api/).

The app must already be subscribed to the WABA, and the alternate HTTPS endpoint
must answer verification and process signed webhooks before setting an override.
The host deliberately does not make a preliminary default subscription, retry a
failed override, or fall back to the app callback. A failure or timeout requires
an operator to inspect provider routing before another activation attempt.

This is **WABA-wide routing**, including other numbers in the same WABA. Confirm
the WABA's complete number inventory before activating it. Template and
account-level events do not support this override and still reach the shared
app callback. This feature does not establish complete webhook isolation.

Trusted server configuration can bind one exact current account to an alternate
endpoint:

```toml
[[whatsapp.webhook_subscription_overrides]]
organization_id = "11111111-1111-4111-8111-111111111111"
account_id = "22222222-2222-4222-8222-222222222222"
business_id = "123456789012345"
callback_url = "https://hooks.example.org/api/webhook"
```

No subscription override is accepted in an HTTP request body. The host uses the
account's existing stored verification token; embedded signup cannot substitute
a browser-supplied token for an explicitly configured override. The current
organization, account UUID and WABA must all match. Changed tuples, duplicate
account/WABA mappings, incomplete configuration and invalid URLs fail before a
subscription call. URLs must be absolute HTTPS, at most 200 characters, without
userinfo or fragments. A new embedded-signup account cannot satisfy a configured
exact account UUID: pre-provision the account and verification token first.

`POST /api/accounts/{id}/subscribe` applies the mapping only when explicitly
invoked. Embedded signup validates it before registration/subscription side
effects. If its explicit override fails, the normal account result is saved
with encrypted credentials, then HTTP 502 reports that routing failed and
dispatch must remain held. Ordinary account subscriptions without a mapping
retain their empty request body and prior behavior. Ordinary sends are unchanged.

The alternate endpoint's `GET /api/webhook` accepts the existing nondeleted
account verification token and returns the challenge unchanged. Unknown or empty
tokens fail. This challenge is not inbound authentication: signed POST ingress
must still validate the app secret and exact current business/phone binding.
Unknown verification tokens and override provider errors are not logged or
returned with their credential values.

After an authorized activation, inspect `GET /{business_id}/subscribed_apps` for
the expected app and `override_callback_uri`, and inspect the phone's effective
`webhook_configuration` for higher-precedence phone overrides. A successful
subscription response proves neither real webhook delivery nor protected case
processing. Keep dispatch held until the dedicated protected receiver, receipt
correlation, current eligibility and deployment gates have been verified.
