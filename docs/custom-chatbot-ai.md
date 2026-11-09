# Custom chatbot AI connections

Custom connections are an opt-in part of the chatbot. They use the same
AI-enabled switch as the built-in providers. The organization default and
WhatsApp account overrides apply to both the automatic AI fallback and the
AI Response node. An account override inherits the other chatbot settings.

## Deployment

Set `WHATOMATE_AI_CONNECTIONS__ENABLED=true` and
`WHATOMATE_AI_CONNECTIONS__ENDPOINTS` to a JSON array of approved endpoints.
The runtime must also have its normal instance encryption key. Keep this
configuration private and restrict it to workspaces that can use the endpoints.
Invalid configuration disables custom connections. Existing assigned scopes
continue with the chatbot's normal non-AI fallback while custom AI is held.

```json
[
  {
    "id": "foundry",
    "label": "Approved Foundry deployment",
    "kind": "foundry",
    "base_url": "https://example.openai.azure.com/openai/v1"
  },
  {
    "id": "local",
    "label": "Private model gateway",
    "kind": "compatible",
    "base_url": "https://model-gateway.example.com/v1",
    "private_ip": "10.25.1.20",
    "blocked_ips": ["10.25.1.4", "10.25.1.6"],
    "local_only": true,
    "models": ["example-model"]
  }
]
```

For Foundry use the deployment name as the model and an inference API key.
For self-hosted inference use a gateway bearer token. The gateway must require
that token, serve trusted TLS on port 443 and expose only the approved model
inference route. Keep Ollama's native port private. For a local-only endpoint,
disable Ollama cloud features and allow only explicitly approved local models.
On a managed host, also enforce network-level egress to the gateway. List
protected platform addresses in `blocked_ips`. Private endpoints accept only
the exact registered IP and reject a mixed or changed DNS response. A change to
the approved policy requires testing the profile again. A changed destination
requires a replacement credential.

## Workspace administration

Open **Settings → Chatbot → AI**. Add a connection, test it, and select it for
the organization default or for a specific account. The connection test makes
one synthetic inference and sends no WhatsApp message. It confirms that the
credential, deployment and Chat Completions API return text. Existing conversation
history and AI context are separate, opt-in inputs. Runtime messages and system
instructions reach the selected provider.

To rotate a credential, add and test a replacement profile, move the
assignments, and remove the old profile. Disabling a profile stops new calls
without changing its assignments. A request already sent to the provider
may finish. To restore service, enable and test it again. Delete is restricted
to unassigned profiles.

Cloud and self-hosted requests have a 30-second model-call deadline and a
non-blocking concurrency limit in the instance's application process. A failed or unavailable custom connection
never switches to a different provider. The automatic fallback keeps its
configured reply behavior, and a flow follows the existing default edge.

Back up the instance database and its encryption key together. For rollback
to a binary from before custom connections, disable chatbot AI for the affected
scopes first if built-in credentials are configured. Leave the additive tables
in place for forward recovery.
