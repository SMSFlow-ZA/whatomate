# Destination-constrained carrier CONNECT proxy

`cmd/extensions-carrier-proxy` is a separate standard-library Go command. It
accepts only HTTP/1 CONNECT with the exact request target
`graph.facebook.com:443`. It has no forward-HTTP support, management or health
endpoint, credential store, TLS termination, request-body inspection, request
logging, provider calls at startup, deployment logic or account activation.

```sh
go build -trimpath -o extensions-carrier-proxy ./cmd/extensions-carrier-proxy
./extensions-carrier-proxy -listen 127.0.0.1:18081 \
  -max-tunnels 4 -tunnel-lifetime 2m -idle-timeout 30s
```

The listener defaults to the explicit loopback address above. The flag accepts a
literal IP and decimal port (1–65535), not a hostname, empty host, scoped address
or environment override. An explicit `0.0.0.0:18081` is permitted for a separately
reviewed isolated container network; this is not permission to publish it.

## Isolation is the caller authorization boundary

There is **no proxy credential authentication**. Only the intended protected app
may reach the listener through a dedicated internal app/proxy network. Do not
publish a host port, expose it publicly, attach another tenant, mount the Docker
socket or connect the app directly to the proxy's external network. Required
network isolation and effective metadata protection belong to the operator's
separate reviewed deployment. The command does not create or verify those network
objects. Root-owned lifecycle controls must preserve them on every start.

A container integration can run the binary as an unprivileged user, with a
read-only filesystem, no mounts/secrets, all capabilities dropped, no-new-privileges
and explicit memory/CPU/PID limits. This source change supplies no image,
deployment, automatic restart, firewall rule, public endpoint or activation.

## Exact destination and DNS boundary

The request target and parsed URL authority must both be the exact lower-case
literal above. Other methods, ports, hostnames, trailing dots, userinfo, schemes,
paths, queries, fragments and encoded authority forms refuse. CONNECT body
framing (`Content-Length`, including zero, transfer coding, trailers, Expect or
Upgrade) refuses, as do extra bytes already buffered before tunnel establishment.
After establishment the stream is opaque; the proxy does not parse or certify
that its bytes are TLS or inspect any tunneled HTTP operation.

Go's standard CONNECT parser derives `Request.Host` from the request target and
ignores the wire `Host` header. A different wire Host therefore does not select a
destination and is not claimed to be separately authenticated or rejected. No
HTTP request headers are forwarded upstream, including Host, cookies or proxy
authorization fields. A regression test verifies that a metadata-address wire
Host still causes only the pinned graph lookup and checked public-IP dial.

Each accepted attempt resolves only `graph.facebook.com`, once. Every returned
address must pass the conservative public-address policy; a mixed public/private
answer refuses entirely. IPv4-mapped IPv6 is checked after unmapping. Private,
loopback, link-local/metadata, multicast, documentation, benchmark, reserved and
transition ranges are excluded. IPv6 is additionally restricted to `2000::/3`,
with special ranges excluded; the Azure host-platform address is denied too.
These are an allow/deny policy over DNS addresses, not a proof of current routing
or Meta's ownership of an arbitrary public address.

The proxy selects one checked answer and dials its literal IP on port443 using
`net.Dialer`; it never dials the hostname again or consults HTTP proxy environment
variables. There is no alternate-address or blind dial retry. DNS and dial each
have a five-second deadline within the absolute attempt lifetime. DNS/dial errors
produce a generic response without upstream error text, addresses or credentials.
Cancellation may close the client before or during that502 response; receiving
an error-response byte is not tunnel establishment. Only a complete200 response
establishes a tunnel.

The client must perform normal end-to-end TLS certificate and hostname
verification for `graph.facebook.com`. Do not disable verification. A poisoned
public DNS answer cannot be made safe by IP filtering alone. TLS, redirects and
application requests remain the client's responsibility: the proxy neither
follows a redirect itself nor authorizes another CONNECT destination. It makes
no at-most-once or application-idempotency guarantee about client retries.

## Bounds and shutdown

Defaults permit four active/resolving tunnels and eight accepted sockets,
including unfinished headers. Excess tunnel capacity gets a generic503; excess
raw sockets close. The tunnel limit is configurable from1 to64; accepted socket
capacity is twice that number. Parsed headers/request metadata are limited to
8KiB and64 header names; this is **not an8KiB raw wire limit**. Go's HTTP parser
has an8KiB configured `MaxHeaderBytes` plus bounded parser read-ahead (4KiB with
the tested pinned Go runtime, approximately12KiB total). The raw-socket regression
observed a complete12,288-byte CONNECT request reaching the handler and a
12,289-byte request receiving431 before DNS. Oversized parsed metadata receives400;
normalized header whitespace can reach the raw parser bound while remaining below
the parsed-metadata limit. These boundary observations include the fixture's
request line and delimiters. Re-run this regression when changing Go versions;
do not describe the configured8KiB value as the total parser allocation/read bound.
Header, pre-hijack
read/write and ordinary HTTP idle timeouts are five seconds.

The absolute tunnel lifetime defaults to two minutes and cannot exceed ten
minutes. Idle timeout defaults to30 seconds, must be positive and cannot exceed
the lifetime. Either direction's successful reads refresh the common activity
deadline. A relay error/EOF closes both sides; this proxy does not preserve a
half-closed tunnel. No packet, header, URL, token, body or upstream error is logged.

SIGINT/SIGTERM refuses new work, cancels outstanding resolution/dial and closes
hijacked tunnels before the HTTP server is drained. No tunnel ownership or
authorization persists across a process restart. It does not restart itself.

## Evidence scope

Tests use injected DNS/dial functions and synthetic `net.Pipe` upstreams. Actual
loopback HTTP sockets exercise parsing, oversized headers, concurrency and
shutdown. A synthetic in-memory certificate verifies transparent end-to-end TLS
and client rejection of a wrong upstream hostname. No test resolves or contacts
Meta. Public-looking test IPs are resolver return values only, never real dial
targets. Tests cover mixed/denied DNS, literal-IP pinning, rebind/no-retry behavior,
authority/body/replay rejection, deadlines, connection capacity and sanitized
errors with the race detector.

This is constrained proxy source, not app egress release, an installed network
boundary, signed webhook/subscription acceptance, valid provider credentials,
an open WhatsApp window, message delivery or live-carrier acceptance. Root's
separate deployment and protected dispatch approvals remain necessary.
