# Relay design

Status: design note, not yet built. Written 2026-09-05. Implements item 3 of PLAN.md §2.4.

## Goal

An everyday user must never open a port, edit a firewall, or read an IP address. The daemon therefore opens *outbound* connections to a relay, the phone connects to the same relay by host id, and the relay forwards bytes it cannot read. LAN and Tailscale stay as faster paths the app tries first; the relay is the path that always works.

The relay must be cheap to run: one small VPS should carry tens of thousands of idle hosts, and scaling further must not require shared state.

## Core design

The reference model is Tailscale's DERP: a dumb relay, end-to-end encrypted, used only when a direct path fails.

### The relay never sees plaintext

The daemon keeps its existing TLS listener and self-signed certificate. Instead of accepting on a TCP port, it also accepts on a custom `net.Listener` whose `Accept` returns virtual connections handed over by the relay. The existing `tls.Server` and WebSocket handler run unchanged on top of it.

The phone keeps pinning the certificate fingerprint from the QR code. Auth stays the Ed25519 challenge bound to the fingerprint and device id. Nothing in protocol v1 changes; the relay is a byte pipe underneath TLS.

The relay's own TLS on port 443 is transport wrapping only. It lets the traffic pass corporate proxies and hotel Wi-Fi and hides metadata on the wire, but confidentiality does not depend on it.

### Two kinds of connection from the daemon

1. **Control socket.** One long-lived outbound WSS per host. The daemon registers its host id, answers pings, and receives "a phone is waiting for you" notices and, later, forwards attention events for push.
2. **Data socket.** When the relay announces a waiting phone, the daemon dials one fresh outbound WSS for that phone. The relay pairs it with the phone's socket and runs `io.Copy` in both directions until either side closes.

### Why one dial per phone instead of multiplexing

- TCP does the flow control. A slow phone stalls only its own stream, never the host's other phones or the control socket.
- The relay holds one 32 KB buffer per direction per stream and no queues, so it cannot be pushed into unbounded memory by a bursting PTY.
- No yamux-style multiplexer with per-stream windows to write, test, and debug on three platforms.

The cost is one extra socket per *active* phone, which is rare compared to idle hosts. iOS suspends sockets within seconds of backgrounding, so active phone streams are short-lived by nature.

### Connection order in the app

Last-good address, then LAN, then Tailscale, then relay. Bytes only cross the relay when nothing else works. In steady state the relay carries heartbeats and nothing else.

## Making one node go far

- **Terminate TLS in the Go process** with `autocert` on :443. A nginx or Caddy hop in front doubles file descriptors, adds a buffering layer, and gains nothing.
- **Idle cost per host** is one goroutine, one `tls.Conn`, and kernel socket buffers. Budget 30 to 50 KB each.
- **Kernel limits.** `LimitNOFILE=1048576` in the systemd unit; raise `net.ipv4.tcp_mem`, `net.core.somaxconn`, and `nf_conntrack_max` (or disable conntrack on the relay box).
- **Pings every 60 s, sent by the client.** This keeps NAT entries alive with the fewest packets and lets the server stay purely reactive. At 100k hosts that is under 2k tiny frames per second.
- **Low-allocation WebSocket library** on the relay side (`gobwas/ws` or `coder/websocket`), no per-message compression, no message reassembly; the relay only moves frames.

Rough capacity for idle hosts on one VPS:

| RAM  | Idle hosts     |
|------|----------------|
| 2 GB | about 40k      |
| 4 GB | 80k to 100k    |

Measure before trusting these; the first load test is a Go program that opens N control sockets from a second box.

## Scaling past one node without shared state

Shard by host id. A small registration endpoint returns the relay hostname for a host, chosen by hashing the host id onto the current shard list (`r1.relay.example`, `r2.relay.example`, ...). The daemon learns its shard at first registration and embeds it in the pairing QR, so the phone always dials the same node. No Redis, no node-to-node forwarding, no sticky sessions at a load balancer.

Adding a shard moves a fraction of hosts; they reconnect to the new hostname on their next registration refresh and their phones pick up the new address through the existing `host.info` reply.

## Push notifications

The relay is the natural place for APNs and FCM. The daemon sends a content-free attention event over the control socket (host id, device tokens, nothing else). The relay sends a wake-only push. The app then reconnects and fetches real state over the encrypted channel. Session names, folders, and output never reach Apple or Google.

## Public API

Small and versioned, shared by the phone app, the desktop app, and self-hosters:

- `POST /v1/hosts` register a host id, returns shard hostname.
- `GET /v1/connect/host/{id}` WebSocket upgrade for the daemon control socket.
- `GET /v1/connect/data/{token}` WebSocket upgrade for a daemon data socket answering a specific waiting phone.
- `GET /v1/connect/device/{hostid}` WebSocket upgrade for a phone; the relay notifies the host and pairs the streams.
- `POST /v1/push` register or refresh device push tokens for a host.

## What not to build

- **Hole punching (WebRTC, QUIC).** Useful later to offload bytes, but the relay is I/O-trivial and hole punching is where the complexity lives.
- **Reusing frp, rathole, Cloudflare Tunnel, or Tailscale Funnel.** Each needs its own account or config per user, which is exactly the friction being removed. The relay above is a few hundred lines of Go sharing the daemon's module.
- **A relay-side account system.** Host ids are random and unguessable; pairing stays on the device. The hosted relay only rate-limits by IP and host id.

## Deployment

One static Go binary, one systemd unit, one 4 EUR Hetzner box to start. Self-hosters run the identical binary and point the daemon at it with `orchestrator config relay https://relay.example`.

## Open questions

- Abuse: cap concurrent data streams per host id and bytes per day on the hosted relay.
- Whether the hosted relay is free, donation-funded, or paid. See PLAN.md §9.
- Whether to keep LAN direct once the relay exists, or drop the inbound listener entirely and simplify firewall handling away.
