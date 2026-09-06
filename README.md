# Orchestrator

Run Claude Code sessions on your own laptop or desktop and drive them from your phone.

- A small background service on the host machine (Linux and macOS today, Windows later) spawns and keeps terminal sessions alive.
- A desktop app installs the service, starts with your computer, lives in the tray, and pairs your phone with a QR code.
- The mobile app (iOS, Android) browses your folders, starts a Claude Code session anywhere, attaches a full terminal, and lets you check back in later. Sessions keep running when the app is closed.

See [PLAN.md](PLAN.md) for the architecture, milestones, and packaging plan.

## Status

The MVP works end to end over the local network: the host daemon (Go, `daemon/`) runs on Ubuntu, and the iOS app (Flutter, `app/`), built as an unsigned IPA by GitHub Actions and sideloaded with iloader, pairs with it by QR code, starts Claude Code in a chosen folder, and drives it from the phone. Verified on an iPhone 17 on 2026-09-05 with build `app-v0.1.2`. See [MVP.md](MVP.md) for what is left before a release.

## Install

> **While this repository is private**, the one-liner and the release links below
> return 404 to anyone not signed in — that includes `curl`. Download with the
> GitHub CLI instead:
>
> ```
> gh release download desktop-v0.1.0 -R markusbug/Orchestrator
> sudo apt-get install -y ./orchestrator_0.1.0_amd64.deb
> ```
>
> Everything below starts working as written the moment the repo goes public.

### Linux

```
curl -fsSL https://raw.githubusercontent.com/markusbug/Orchestrator/main/scripts/install.sh | sh
```

That fetches the latest release: the `.deb` when `dpkg` is available, the AppImage otherwise. Both are also on the [releases page](https://github.com/markusbug/Orchestrator/releases). Then open **Orchestrator** from your applications menu, or run `orchestrator`.

Two notes. On arm64 the installer sets up the daemon alone and pairs from the terminal — the desktop app is x86_64-only for now. And on GNOME the tray icon needs the AppIndicator extension, which Ubuntu ships and enables by default; on other GNOME systems enable it with `gnome-extensions enable ubuntu-appindicators@ubuntu.com`. Without a tray, closing the window quits the app instead of hiding it — the background service keeps running either way.

### macOS

Download the `.dmg` from the [releases page](https://github.com/markusbug/Orchestrator/releases) and drag Orchestrator to Applications.

The app is **not signed or notarized** — there is no Apple Developer Program subscription yet — so macOS refuses to open it on the first try. Either clear the download flag:

```
xattr -dr com.apple.quarantine /Applications/Orchestrator.app
```

…or double-click, let macOS refuse, then open **System Settings → Privacy & Security** and click **Open Anyway**. (On macOS 15 and later, right-click → Open no longer offers a bypass.)

The macOS build is produced and signature-checked by CI but has not been run on a real Mac. Treat it as beta.

### Windows

Not yet — see [docs/DESKTOP.md](docs/DESKTOP.md). The daemon has no ConPTY support, so the whole host side is Unix-only today.

## First run

The window offers one button to start the background service, then shows a QR code. Scan it with the phone app and you are done. Closing the window leaves Orchestrator in the tray or menu bar; quitting from there leaves your sessions running.

## Developing the daemon

```
make build
./bin/orchestrator serve --debug      # foreground, debug web client at https://localhost:7391/_debug/
./bin/orchestrator install            # run as a user service instead
```

`orchestrator` on its own opens the desktop app. The older subcommands (`status`, `pair`, `devices`, `sessions`, `relay`, `logs`) still work and are the way to drive a headless machine over SSH; they are no longer in the usage text because the app is the supported route.

Developer note: if a firewall is active on the host (`ufw status`), allow the port with `sudo ufw allow 7391/tcp`, otherwise the phone's connection is silently dropped. Users of the finished product never need this: the daemon connects outbound to the hosted relay at `relay.markushaas.com` by default, so phones reach it from anywhere without any port or firewall step (see [docs/RELAY.md](docs/RELAY.md)). The TLS certificate is self-signed; the phone pins its fingerprint at pairing time, and the browser will ask you to accept it once. `--debug` skips authentication for connections from the same machine, so only use it on a machine you trust.

## Run a relay

The relay removes every port and firewall step for end users: the daemon connects out, phones connect to `<hostid>.<relay-domain>:443`, and the relay pipes the daemon's own TLS through without seeing plaintext. `make build-relay` builds it; `make run-relay-dev` runs a self-signed one locally. Design in [docs/RELAY.md](docs/RELAY.md), deployment and operations in [deploy/relay/README.md](deploy/relay/README.md). Daemons use the hosted relay `https://relay.markushaas.com` by default; `orchestrator relay` shows the connection state, `orchestrator relay set https://relay.example` points at a self-hosted one, and `orchestrator relay off` disables it.

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
