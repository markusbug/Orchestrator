# Orchestrator

Run Claude Code sessions on your own laptop or desktop and drive them from your phone.

- A small background service on the host machine (Linux, macOS, Windows) spawns and keeps terminal sessions alive.
- A desktop companion app installs the service and pairs your phone with a QR code.
- The mobile app (iOS, Android) browses your folders, starts a Claude Code session anywhere, attaches a full terminal, and lets you check back in later. Sessions keep running when the app is closed.

See [PLAN.md](PLAN.md) for the architecture, milestones, and packaging plan.

## Status

The MVP works end to end over the local network: the host daemon (Go, `daemon/`) runs on Ubuntu, and the iOS app (Flutter, `app/`), built as an unsigned IPA by GitHub Actions and sideloaded with iloader, pairs with it by QR code, starts Claude Code in a chosen folder, and drives it from the phone. Verified on an iPhone 17 on 2026-09-05 with build `app-v0.1.2`. See [MVP.md](MVP.md) for what is left before a release.

## Try the daemon

```
make build
./bin/orchestrator serve --debug      # foreground, debug web client at https://localhost:7391/_debug/
./bin/orchestrator status
./bin/orchestrator pair               # QR code for the phone app
./bin/orchestrator install            # run as a systemd user service instead
```

Developer note: if a firewall is active on the host (`ufw status`), allow the port with `sudo ufw allow 7391/tcp`, otherwise the phone's connection is silently dropped. Users of the finished product will never do this; the daemon will connect outbound through a relay with a public API so everything works out of the box (see PLAN.md, Connectivity). The TLS certificate is self-signed; the phone pins its fingerprint at pairing time, and the browser will ask you to accept it once. `--debug` skips authentication for connections from the same machine, so only use it on a machine you trust.

## Run a relay

The relay removes every port and firewall step for end users: the daemon connects out, phones connect to `<hostid>.<relay-domain>:443`, and the relay pipes the daemon's own TLS through without seeing plaintext. `make build-relay` builds it; `make run-relay-dev` runs a self-signed one locally. Design in [docs/RELAY.md](docs/RELAY.md), deployment and operations in [deploy/relay/README.md](deploy/relay/README.md). Point a daemon at a relay with `orchestrator relay set https://relay.example`, or turn it off with `orchestrator relay off`.

## Try the app

```
cd app
flutter pub get
flutter analyze && flutter test            # unit tests plus a fake daemon over TLS
```

To exercise the app's networking against the real daemon, run `orchestrator pair` and pass its output to the live test:

```
ORCH_LIVE_PORT=7391 ORCH_LIVE_CODE=123456 ORCH_LIVE_FP=sha256:... flutter test test/live_test.dart
```

The iOS build runs in GitHub Actions on `app-v*` tags (`.github/workflows/ios.yml`) and produces an unsigned IPA for sideloading. See [MVP.md](MVP.md) for the protocol, scope, and sideloading steps.
