# Orchestrator

Run Claude Code sessions on your own laptop or desktop and drive them from your phone.

- A small background service on the host machine (Linux, macOS, Windows) spawns and keeps terminal sessions alive.
- A desktop companion app installs the service and pairs your phone with a QR code.
- The mobile app (iOS, Android) browses your folders, starts a Claude Code session anywhere, attaches a full terminal, and lets you check back in later. Sessions keep running when the app is closed.

See [PLAN.md](PLAN.md) for the architecture, milestones, and packaging plan.

## Status

The host daemon (Go, `daemon/`) runs on Ubuntu. The phone app is not started yet.

## Try the daemon

```
make build
./bin/orchestrator serve --debug      # foreground, debug web client at https://localhost:7391/_debug/
./bin/orchestrator status
./bin/orchestrator pair               # QR code for the phone app
./bin/orchestrator install            # run as a systemd user service instead
```

The TLS certificate is self-signed; the phone pins its fingerprint at pairing time, and the browser will ask you to accept it once. `--debug` skips authentication for connections from the same machine, so only use it on a machine you trust.

See [MVP.md](MVP.md) for the protocol and scope.
