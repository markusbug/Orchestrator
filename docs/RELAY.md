# Relay design

Status: implemented in `daemon/internal/relay` and `daemon/cmd/relay` (2026-09-06); not yet deployed. Operations: [deploy/relay/README.md](../deploy/relay/README.md). Implements item 3 of PLAN.md §2.4.

## Goal

An everyday user must never open a port, edit a firewall, or read an IP address. The daemon therefore opens *outbound* connections to a relay, the phone connects to the same relay by host id, and the relay forwards bytes it cannot read. LAN and Tailscale stay as faster paths the app tries first; the relay is the path that always works.

The relay must be cheap to run: one small VPS should carry tens of thousands of idle hosts, and scaling further must not require shared state.

## Core design

The reference model is Tailscale's DERP: a dumb relay, end-to-end encrypted, used only when a direct path fails.

```
phone ──TLS, SNI = <hostid>.relay.example──▶ relay:443 ──raw bytes over a WSS data socket──▶ daemon's TLS listener
daemon ──WSS control socket──▶ relay:443 (SNI = relay.example, Let's Encrypt certificate)
```

### The relay never sees plaintext

The daemon keeps its existing TLS listener and self-signed certificate. Besides its TCP port it also accepts on a virtual `net.Listener` (`internal/relay/vconn`) whose connections are handed over by the relay. The same `http.Server` serves both, so protocol v1 runs unchanged.

The phone connects to `<hostid>.<relay-domain>:443` with plain TLS and pins the daemon's certificate fingerprint from the QR code, exactly as on the LAN. The relay reads only the TLS ClientHello, routes on its SNI, and pipes bytes. It cannot read or alter the session, and it cannot route a phone to a wrong daemon without the pin failing.

Why not a WebSocket for the phone leg: the app's TLS (dart:io) only runs over real sockets, so the phone cannot run TLS inside a WebSocket. Raw TLS routed by SNI needs no app-side crypto change at all: the relay address is one more entry in the host's address list, with its own port.

The relay's own TLS on the apex (`relay.example`) is transport wrapping for the daemon-facing API. It lets daemon traffic pass corporate proxies as ordinary HTTPS; confidentiality does not depend on it.

### Host identity

Each daemon has an Ed25519 host key (`<config dir>/host_key.pem`). Its host id is the first 16 bytes of the SHA-256 of the public key as lowercase hex: 32 characters, a valid DNS label. The relay stores nothing about hosts. Every control connection proves ownership of its id by signing a challenge bound to the relay's domain, so a signature is never valid at another relay and squatting an id would need a second preimage.

### Two kinds of connection from the daemon

1. **Control socket.** One long-lived outbound WSS per host to `GET /v1/connect/host/{id}`. The relay sends `challenge{nonce, relay}`, the daemon answers `auth{pubkey, sig, version}`, the relay replies `ok{ping_interval_s, max_streams}`. The daemon then pings every 60 s; the relay closes hosts silent for 150 s. A daemon that authenticates for an id already online replaces the old socket (close code 4409); an unauthenticated connection never evicts anyone. The daemon may also send `push{platform, tokens}` (reserved, see below).
2. **Data socket.** When a phone connects for host X the relay records the ClientHello (5 s deadline, 16 KiB cap), creates a single-use 256-bit token, sends `dial{token, peer}` on X's control socket, and waits 10 s. The daemon dials `GET /v1/connect/data/{token}`; the relay replays the recorded ClientHello and then copies bytes both ways with 32 KiB buffers until either side closes or the stream idles for 10 min. The daemon answers `busy{token}` when at its stream cap so the phone fails fast. Three unanswered dials in a row close the control socket (4408).

### Why one dial per phone instead of multiplexing

- TCP does the flow control. A slow phone stalls only its own stream, never the host's other phones or the control socket.
- The relay holds one buffer per direction per stream and no queues, so a bursting PTY cannot push it into unbounded memory.
- No multiplexer with per-stream windows to write and debug on three platforms.

The cost is one extra socket per *active* phone, which is rare compared to idle hosts. iOS suspends sockets within seconds of backgrounding, so active phone streams are short-lived by nature.

### Connection order in the app

Last-good address, then LAN, then Tailscale, then relay. Bytes only cross the relay when nothing else works. In steady state the relay carries heartbeats and nothing else.

### Reconnect policy in the daemon

Backoff 1 s doubling to 60 s with jitter. After close code 1012 (relay restarting) the wait is uniform 1 to 30 s so 100k hosts do not redial in the same five seconds. After 4409 (another daemon holds this host key, e.g. a copied config directory) it waits 30 to 60 s and logs a warning. After 4401 (rejected) it waits 5 min.

## Limits (in memory, per relay node)

Pending dials per host 8, active streams per host 16, per-IP token bucket of 30 new connections per minute, per-IP concurrent ClientHello peeks 16, control-auth lockout after 5 failures in 10 min, global connection cap at 90 % of the file-descriptor limit. Phones for unknown or offline hosts are closed before any TLS ServerHello, so they cost the relay nothing beyond the peek. Per-host byte counters exist; enforcement of a daily quota is an open question below.

## Making one node go far

- TLS for the apex terminates inside the Go process with `autocert` (TLS-ALPN-01). A nginx or Caddy hop in front would double file descriptors and add buffering for no gain.
- Idle cost per host is one goroutine, one `tls.Conn`, and kernel socket buffers. Budget 30 to 50 KB each.
- Pings are sent by the daemon every 60 s and tracked with the WebSocket library's ping callback, so the relay stays purely reactive. At 100k hosts that is under 2k tiny frames per second.
- Kernel keepalive on accepted sockets is 5 min idle, 30 s interval, 5 probes. Go's default 15 s would be ~7k probe packets per second at 100k hosts.
- `deploy/relay/90-relay.conf` raises the fd limit and sizes TCP memory; the unit sets `LimitNOFILE=1048576`.

Rough capacity for idle hosts on one VPS (to be replaced by measured numbers from `cmd/relayload`):

| RAM  | Idle hosts     |
|------|----------------|
| 2 GB | about 40k      |
| 4 GB | 80k to 100k    |

## Scaling past one node without shared state

Host ids live in SNI subdomains, so a second node is a second domain: hosts told to use `https://r2.relay.example` connect there, their phones learn `<hostid>.r2.relay.example` through the pairing QR and `host.info`, and the two nodes share nothing. Moving a host is a config change and a restart. Nothing in the protocol changes.

## Push notifications (reserved, not built)

The relay is the natural place for APNs and FCM once an Apple Developer account exists. The phone will hand its push token to the *daemon* over the end-to-end channel; the daemon forwards tokens in a content-free `push` message and the relay sends a wake-only notification. The app then reconnects and fetches real state. Session names, folders, and output never reach Apple or Google, and the relay still keeps no state. Today the relay accepts `push` and drops it.

## Public API (apex, HTTPS)

- `GET /healthz` returns `{"ok":true,"version":"..."}`; `ok` is false while draining.
- `GET /v1/hosts/{id}` returns `{"online":bool}` so the app can show presence without dialling.
- `GET /v1/connect/host/{id}` WebSocket upgrade for a daemon's control socket.
- `GET /v1/connect/data/{token}` WebSocket upgrade for a daemon's data socket answering one dial.

Phones do not use the API; they connect to `<hostid>.<domain>:443` with TLS. On the loopback metrics port the relay also serves `/healthz`, `/debug/vars` (expvar) and `/debug/pprof/`.

## What not to build

- **Hole punching (WebRTC, QUIC).** Useful later to offload bytes, but the relay is I/O-trivial and hole punching is where the complexity lives.
- **Reusing frp, rathole, Cloudflare Tunnel, or Tailscale Funnel.** Each needs its own account or config per user, which is exactly the friction being removed.
- **A relay-side account system.** Host ids are unguessable and proven by signature; pairing stays on the device. The hosted relay only rate-limits.

## Deployment

One static Go binary, one systemd unit, one small Hetzner box. `deploy/relay/` holds the unit, kernel tuning, an install/upgrade/rollback script and the runbook. A `relay-v*` tag publishes `relay_linux_amd64`, `relay_linux_arm64`, `relay-deploy.tar.gz` and checksums on a GitHub release. Self-hosters run the identical binary and point the daemon at it with `orchestrator relay set https://relay.example`; the hosted relay is compiled in as the default once it exists (`config.DefaultRelayURL`).

## Open questions

- Abuse: enforce bytes per host per day on the hosted relay (counters exist).
- Whether the hosted relay is free, donation-funded, or paid. See PLAN.md §9.
- Whether to keep the LAN listener once the relay exists, or drop inbound entirely and simplify firewall handling away.
