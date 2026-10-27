# Duplicate a chatbot flow

In **Chatbot → Conversation Flows**, use **Duplicate flow** beside a flow. The
new flow is named `<original> (Copy)` and starts **inactive**. Review its name,
keywords, channel, graph and completion actions before enabling it. An SMS flow
remains subject to connection permissions, consent and the dispatch emergency
stop when later enabled.

Copies preserve the configuration and graph, including node identifiers and
edges within that graph. They have a new flow identifier and creation/update
actor. Sessions, conversation history and queued messages are not copied. The
source flow is unchanged. Both `flows.chatbot:read` and `flows.chatbot:write`
permissions are required, within the same organization.

API clients can call `POST /api/chatbot/flows/{id}/duplicate` without a body.
The normal authentication and browser CSRF protections apply. The response
contains `id`, `message` and `enabled: false`. Each successful request creates
a separate disabled copy; clients should not automatically retry an ambiguous
response without checking the flow list.

No schema migration or additional service is required.
