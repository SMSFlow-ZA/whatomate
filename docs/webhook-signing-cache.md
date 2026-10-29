# Webhook signing and Redis caching

Webhook deliveries use `X-Webhook-Signature: sha256=<hex>` over the exact request
body, using the webhook secret. The secret remains excluded from public model
JSON. A server-only encrypted Redis envelope preserves it on subsequent cache
hits, together with any private custom headers. The envelope is bound to the
organization and has the existing six-hour TTL.

Set `[app].encryption_key` (or `WHATOMATE_APP__ENCRYPTION_KEY`) to use the encrypted
cache. Without a key, webhook lookups use PostgreSQL directly. Redis failures,
old plaintext entries, corrupt entries and entries encrypted with a previous key
reload from the database. A database lookup failure stops dispatch rather than
using incomplete cached settings. Changing or disabling a webhook invalidates
its organization's cache as before.

This replaces the old model-JSON cache, which omitted the signing secret and
therefore signed only the first delivery after a cache miss. No schema migration
or receiver change is required. Receivers should verify the HMAC against the raw
body before processing an event.
