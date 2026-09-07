# Security

## Reporting a vulnerability

Report privately through GitHub: **Security → Advisories → Report a vulnerability**
on [this repository](https://github.com/markusbug/Orchestrator/security/advisories/new).
Please do not open a public issue for anything exploitable.

Include what you did, what happened, and which component (daemon, relay, or app).
A proof of concept helps but is not required. This is a side project maintained by
one person: expect a first reply within a week, and no bug bounty.

## What this software does

Orchestrator gives a paired phone a terminal on your machine.

**A paired device can run any command as the user running the daemon.** That is the
product, not a bug. There is no sandbox, no command allowlist, and no confirmation
step. Anyone who pairs a device has the same access to your machine as anyone sitting
at your keyboard: your files, your SSH keys, your cloud credentials, your shell.

Treat pairing a device the way you would treat handing someone a logged-in laptop.
The security of the whole system reduces to one question — *did only my devices ever
pair?*

In particular, `roots` in `config.toml` is **not** a sandbox. It restricts which
directories the folder browser lists and which directory a session can start in.
It does not restrict what a session may then do; `cmd` and `args` are arbitrary,
and a shell started in an allowed directory can read and write anywhere the user can.

## Trust boundaries

| Party | Can do |
| --- | --- |
| A paired device | Everything the daemon's user can do on that machine |
| The relay operator | See that a host id is online, when it connects, and how many bytes flow. **Not** read or modify any traffic |
| Someone on your LAN | Reach the daemon's port and attempt pairing or authentication, subject to the limits below |
| Someone who knows your host id | The same, from anywhere on the internet, while the relay is enabled |
| Another user account on your machine | Nothing through Orchestrator's own interfaces (see *Local access*), though a local user with your privileges has no need of them |

## How the pieces protect themselves

**Transport.** The daemon serves TLS 1.3 only, using a self-signed P-256 certificate
generated on first run and kept at `tls/cert.pem` in the config directory. The app
pins the SHA-256 of that certificate, learned from the pairing QR code, and disables
the system trust store entirely — the pin is the only authority. A certificate that
does not match the pin ends the connection and is surfaced to the user rather than
retried.

**Pairing.** `orchestrator pair` issues a random 6-digit code that lives 5 minutes,
is single-use, and is destroyed after 5 wrong guesses in total — not 5 per attacker.
Guessing is therefore bounded at 5 tries per code, not by how many connections an
attacker can open. The QR code carries the addresses, the port, the certificate
fingerprint, and the code.

**Device authentication.** On pairing, the phone generates an Ed25519 key pair in the
platform keystore (iOS Keychain / Android Keystore via `flutter_secure_storage`) and
registers the public key. The key is stored device-only
(`kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly` on iOS), so it is not carried in
an encrypted iTunes/Finder backup and cannot be restored onto a different phone —
restoring a backup means pairing again. Every later connection signs a challenge built from
`"orch-auth-v1"`, a fresh 32-byte server nonce, the client's nonce, the server's
certificate fingerprint, and the device id. Server nonces are single-use, and binding
the fingerprint into the signature stops a signature captured on one host from being
replayed to another. The private key never leaves the phone.

**Rate limiting.** Five failed pairings or authentications from one IP lock that IP
out for ten minutes. Addresses reported by a relay are keyed separately from direct
ones, so traffic through a relay cannot lock out or unlock a LAN address. Limiter
state is swept and capped, so a flood of distinct addresses cannot grow it without
bound. A connection that has not authenticated within 30 seconds is closed, and at
most 64 unauthenticated connections are accepted at a time.

**The relay.** The relay routes on the TLS ClientHello's SNI and never terminates
host TLS: for `<hostid>.<domain>` it copies bytes between the phone's socket and a
WebSocket the daemon dialled outbound, so the plaintext is inside the daemon's own
TLS the whole way. It cannot read or alter session traffic, and it holds no key that
would let it. A daemon proves ownership of its host id with an Ed25519 signature over
a relay-issued nonce; the host id is the first 16 bytes of the SHA-256 of its public
key, so it cannot be claimed by anyone else.

What the relay *does* see: which host ids are online, connection timing, and byte
counts. `GET /v1/hosts/<id>` answers whether a host id is online to anyone who asks.
Host ids are 128-bit and not enumerable, but anyone who has seen one — from a QR code
or a shared screen — can poll it.

**Local access.** Private keys and the host key are written `0600`, the config
directory tree `0700`. The admin socket (used by the CLI and by Claude Code hooks)
has no authentication beyond its file permissions: it is `0600` inside a directory
only its owner can enter, including in the fallback location used when the config
path is too long for a unix socket. That socket can issue pairing codes, so a process
that can reach it can pair a new device.

## Things worth knowing before you deploy this

**The relay is on by default**, pointing at `relay.markushaas.com`. This means the
daemon's authentication surface is reachable from the internet by anyone holding your
host id, not just from your LAN. That is deliberate — it is what makes the product
work without port forwarding — but it is the single biggest thing to be aware of.
Turn it off with `orchestrator relay off` if you only ever use the daemon on a
trusted network, and it will remain reachable over LAN and Tailscale.

**`--debug` disables authentication for connections from the same machine** and serves
a browser debug client at `/_debug`. It also permits plaintext `http://` relay URLs.
Use it only on a machine you trust, and never leave `debug = true` in `config.toml`.
Connections arriving through a relay are never treated as loopback, whatever address
the relay reports.

**Certificate pinning has no rotation path.** If you delete the daemon's `tls/` files
or the key leaks, every paired phone hard-fails with a fingerprint mismatch and must
be re-paired. There is no revocation of a certificate short of that.

**Revoke devices you no longer use** with `orchestrator devices` — a device key is
valid until revoked. Revocation takes effect on the device's next connection.

**Session scrollback and a one-line preview of each session are held in memory** and
are readable by any paired device. Anything Claude Code prints — including secrets it
was shown — is visible to every device paired to that host.

## Non-goals

- **Multi-user isolation.** One daemon serves one user account. It does not attempt to
  separate paired devices from one another: every device sees every session.
- **Sandboxing sessions.** Sessions run with the daemon's full privileges by design.
- **Protecting against a hostile relay for metadata.** A relay operator learns who is
  online and when. Run your own relay if that matters (see [docs/RELAY.md](docs/RELAY.md)).
- **Protecting against a compromised phone.** A device key extracted from an unlocked,
  compromised phone is as good as the phone. Backups are covered — the key is
  device-only, so it is not in one — but a phone an attacker can read is not.

## Running your own relay

The relay in `deploy/relay/` runs under a hardened systemd unit (`DynamicUser`,
`ProtectSystem=strict`, a syscall filter, no ambient capabilities beyond
`CAP_NET_BIND_SERVICE`). Its metrics and `pprof` endpoints must stay on loopback —
`RELAY_METRICS_LISTEN` defaults to `127.0.0.1:9100` and there is no authentication in
front of them. The relay applies per-IP connection rate limits, caps concurrent TLS
handshake inspections, and bounds pending dials and active streams per host.

## Supported versions

Only the latest release of each component (daemon, relay, app) receives fixes.
