# Orchestrator MVP

Target user: someone who already runs Claude Code daily and wants to start, watch, and nudge sessions on their own machine from their phone. They are comfortable with a terminal and can install Tailscale.

The first version targets exactly one host platform and one phone platform:

- **Host: Ubuntu** (the developer's own laptop). macOS host support comes right after; the code is written portably from day one.
- **Phone: iPhone**, built in GitHub Actions on a macOS runner and sideloaded with a free Apple ID. Android comes after the iOS app works.
- **Same network only.** For the MVP the phone and the host must be on the same Wi-Fi/LAN. Remote access via Tailscale is the first post-MVP item; the daemon already lists Tailscale addresses in the QR when it finds one, so nothing in the protocol changes.

The MVP is done when this works end to end:

> Install the daemon on an Ubuntu laptop with one command. Pair the iPhone by scanning a QR code printed in the terminal. From the phone, on the same Wi-Fi, browse to a project folder, start Claude Code there, give it a task, lock the phone, come back an hour later, and pick up the same session with its screen intact.

## Progress

Updated 2026-09-05. Tick items here as they land so this file stays the single source of truth for what the MVP still needs.

### Host daemon (Ubuntu) — done

- [x] Session manager: PTY spawn, 1 MB scrollback, multi-attach, resize with SIGWINCH nudge, kill, rename, remove, exit codes, output preview
- [x] Status machine running / waiting / exited / stale; graceful shutdown records sessions as stale
- [x] Claude Code integration: `--session-id` at launch, `--settings` hooks file, Notification / Stop / UserPromptSubmit hooks → status, bell fallback, `session.resume` with `--resume`, conversation list from `~/.claude/projects`
- [x] Protocol v1: JSON envelopes with `rid`, binary PTY frames with per-run handles, events (`session.event`, `session.removed`, `session.detached`)
- [x] TLS with persisted self-signed cert and fingerprint; QR pairing with single-use 6-digit code (5 min, 5 attempts); Ed25519 challenge auth bound to nonces, fingerprint, and device id; per-IP lockout; device revoke
- [x] Filesystem API under allow-listed roots (list, search, recents) with symlink-escape protection
- [x] SQLite metadata store (sessions, devices, recents); all non-exited sessions marked stale on startup
- [x] Local admin socket for the CLI and hooks; short-path fallback for long config dirs
- [x] CLI: `serve`, `install`, `uninstall`, `status`, `pair` (terminal QR), `devices [revoke]`, `sessions [kill|remove]` with id prefixes, `logs`, `_hook`
- [x] systemd user unit with the installer's PATH and linger (implemented; not yet run on the dev laptop)
- [x] Embedded xterm.js debug client at `/_debug/` with loopback auto-auth under `--debug`
- [x] Tests: unit tests per package, WebSocket integration tests, race-clean; `go test -tags live` end-to-end against a running daemon with real Claude Code (hooks, reconnect replay, kill verified)
- [x] CI: gofmt, vet, race tests, darwin/arm64 and linux/arm64 cross-compile, Linux binary artifact — green on `main`

### Host daemon — remaining for MVP

- [ ] Run `orchestrator install` on the dev laptop and confirm it survives logout and reboot (acceptance item)
- [ ] Firewall handling: `install` detects an active `ufw`/firewalld deny-incoming policy and adds the allow rule (or prints the exact command); `status` warns when the port is blocked. Found during the first phone pairing: ufw silently dropped port 7391. This is a stopgap; end users must never touch ports, see PLAN.md 2.4 for the outbound relay and API that make it work out of the box.
- [ ] Release pipeline: goreleaser config, `scripts/install.sh`, first tagged `linux/amd64` + `linux/arm64` binaries (build order step 7). The relay already has its own: `relay-v*` tags build and release `relay_linux_{amd64,arm64}` via `.github/workflows/relay.yml`.
- [x] Relay (post-MVP item pulled forward, 2026-09-06): `daemon/cmd/relay`, daemon-side client, `orchestrator relay` CLI, `deploy/relay/` unit + install script, app dials relay-kind addresses. See docs/RELAY.md. Not yet deployed to a VPS.
- [ ] Local notification path for the app relies on nothing server-side; no work needed

### iOS app — installed and working on the phone

Flutter project in `app/`, verified on Ubuntu with `flutter analyze`, `flutter test` (fake daemon over TLS), and `test/live_test.dart` against the real Go daemon (pair, auth, create, attach, replay, resize, rename, kill, resume, remove).

**On-device result (2026-09-05):** the `app-v0.1.2` IPA from GitHub Actions was signed and installed with iloader 2.3.1 from Ubuntu on an iPhone 17 (iOS 26.6.1). Over the local Wi-Fi, the phone paired with the laptop daemon, listed sessions, started Claude Code in a chosen folder, and drove it from the terminal with prompts submitted from the on-screen keyboard. **The MVP end-to-end flow works over the local network.** Three tagged builds were made in one evening (v0.1.0 pipeline proof, v0.1.1 return key submits + New line key, v0.1.2 plain keyboard layout), each about 4 minutes of macOS runner time.

- [x] Flutter project skeleton in `app/` (build order step 3)
- [x] First IPA built by `ios.yml` (5 min) and sideloaded with iloader 2.3.1 on 2026-09-05; Developer Mode prompt on iOS 26 behaves as documented
- [x] `.github/workflows/ios.yml` producing an unsigned IPA on `app-v*` tags; `.github/workflows/app.yml` runs format, analyze, and tests on Linux
- [x] Pairing by QR (`mobile_scanner`) + manual entry; Ed25519 keys in the Keychain (`flutter_secure_storage`); fingerprint pinning with system roots disabled
- [x] Connection manager: one socket per host, last-good then LAN then Tailscale, exponential backoff with jitter, reconnect on foreground and network change, ping probe, auto re-attach with replay
- [x] Hosts screen (online dot, running/waiting counts, waiting badge, rename/forget), Sessions screen (sorted cards, preview, swipe to kill/remove, long-press rename, pull to refresh)
- [x] New session flow: recents, breadcrumb browser with `.git` markers and hidden toggle, search, command sheet with resume list
- [x] Terminal screen: xterm.dart, key bar (Esc, Tab, sticky Ctrl, arrows, /, ⇧Tab, paste, and optional ^C / Enter) with a pinned hide/show keyboard button, pinch zoom, copy selection, swipe on the title bar between sessions, force kill
- [x] Settings: font size, theme, key bar order, haptics, notifications, device name, per-host rename / forget
- [x] Stale resume button; in-app badges; local notification while backgrounded and connected (`flutter_local_notifications`)
- [x] On-device: pairing, permissions, and the terminal work on an iPhone 17 / iOS 26. First bug found and fixed: the soft keyboard's return key inserted a line feed, which Claude Code treats as "new line", so prompts could not be submitted. `xterm` 4.0.0 is vendored in `app/third_party/xterm` with a small patch (see its PATCHES.md) so the return key is "Send" (carriage return) and the key bar has a "New line" key. Second bug: the terminal could not be scrolled and the keyboard could not be dismissed. Claude Code 2.1.x draws in the alternate screen with mouse tracking and scrolls its own transcript on wheel reports; on iOS xterm's empty scrollback view swallowed every drag, and its wheel reports carried the shift bit. Both are patched in the vendored xterm (PATCHES.md), and the key bar has a keyboard toggle. Not yet verified on the phone.
- [ ] On-device pass continues: resize on rotation and keyboard show/hide, notifications in the background, reconnect after Wi-Fi toggling, 10 sessions

### Acceptance checklist status

See the checklist below. Ticked items were verified by hand on 2026-09-05 with the phone on the same Wi-Fi as the laptop. Host-only items that can already be verified from the daemon tests: sessions survive the app closing (daemon holds them), 10 concurrent sessions, hook-driven *waiting* within 2 seconds, revoke disconnects, daemon restart marks sessions stale and resume works. The unticked phone items have working code (exercised against the real daemon by `app/test/live_test.dart`) but have not been walked through on the device yet.

## Scope

**In**

- Host daemon (Go) as a CLI, running as a systemd user service on Ubuntu.
- iOS app (Flutter), sideloaded with a free Apple ID.
- Direct connection over the local network. Nothing else.
- Any number of sessions per host, any number of hosts per phone.
- GitHub Actions workflow that produces an unsigned `.ipa` on every tag.

**Out (explicitly, until after MVP)**

- Remote access from outside the LAN (Tailscale first, relay later). The relay is also what removes every firewall and port step for end users; the MVP's `ufw allow` hint is developer-only scaffolding, not the product.
- macOS and Windows hosts. Design for them, ship after Ubuntu works.
- Android build. Same Flutter code, enabled once the iOS app is usable.
- ~~Desktop GUI, tray app, setup wizard.~~ Built after the MVP; see [docs/DESKTOP.md](docs/DESKTOP.md).
- ~~Native installers (.deb, .dmg, .exe).~~ `.deb`, AppImage and `.dmg` ship on `desktop-v*` tags; Windows is still out.
- Relay server, accounts, billing, push notifications. Push is also impossible on a free Apple ID.
- App Store / TestFlight distribution. Requires the paid Apple Developer Program.
- Structured/chat rendering of Claude Code. Terminal only.
- Session survival across daemon restarts. Stale sessions are marked and can be resumed with one tap.

## Host side (daemon, Ubuntu)

### Commands

```
orchestrator serve              run in foreground (used by the service)
orchestrator install            register systemd user unit and start it
orchestrator uninstall
orchestrator status             running? port? address(es)? paired devices? session count?
orchestrator pair               print QR + code in terminal, valid 5 minutes
orchestrator devices [revoke]   list or revoke paired phones
orchestrator sessions [kill]    list or kill sessions
orchestrator logs               tail the log file
```

### Must have

- **Sessions.** Spawn a PTY with cwd, command, args, cols, rows. Default command is `claude`. Track id (UUID), a per-run `uint32` handle for binary frames, name, cwd, pid, status (running, waiting, exited, stale), exit code, created, last output time, and a one-line preview of the latest output.
- **Scrollback.** Per-session ring buffer, 1 MB default. On attach: replay buffer, then apply the client's size so the TUI redraws.
- **Multi-attach.** More than one client on the same session at once. Last resize wins.
- **Metadata store.** SQLite file under the config dir. Every non-exited session becomes *stale* on daemon startup (closing the PTY kills its child), and keeps its cwd, command, and Claude session id for resume. A graceful daemon shutdown also records its sessions as stale, not exited.
- **Filesystem API.** List a directory (dirs first, hidden toggle, `.git` marker), stat, and a bounded recursive name search. Roots default to the home directory.
- **Claude conversation list.** Read `~/.claude/projects/<encoded cwd>/*.jsonl` to list previous conversations for a folder with their first prompt and timestamp, so the phone can offer *resume* when creating a session. The directory name replaces every byte outside `[A-Za-z0-9_]` with `-`; it is lossy, so always encode, never decode.
- **Resume strategy.** Claude sessions launch with `--session-id <uuid>` so the conversation id is known up front. `session.resume` relaunches a stale or exited session in the same folder with `--resume <uuid>` (or `--continue` if no id is known) and removes the old row.
- **Attention flag.** Launch `claude` with `--settings <generated json>` that adds `Notification`, `Stop`, and `UserPromptSubmit` hooks calling `<abs path>/orchestrator _hook <event>` over the local admin socket; the session id travels in `ORCHESTRATOR_SESSION_ID`. Notification and Stop set *waiting*; UserPromptSubmit and any client input set *running*. Fallback: terminal bell.
- **Transport.** WebSocket over TLS on a configurable port (default 7391). Self-signed cert generated on first run, fingerprint printed by `status` and embedded in the QR.
- **Auth.** Pairing: QR carries `{addresses, port, fingerprint, code}`. Phone connects, presents code and its public key, daemon stores it. Later connections: challenge signed by the phone's key. Rate limit failures. Codes are single-use.
- **Addresses in QR.** All non-loopback IPv4/IPv6 addresses, with Tailscale addresses marked as such so the app prefers them when off-LAN.
- **Local admin socket.** Unix socket for CLI subcommands and hooks. No auth beyond file permissions.
- **Service install.** systemd user unit with `Restart=on-failure` and the installing user's PATH baked in (so `claude` resolves), enabled with `loginctl enable-linger` so it survives logout. Logs go to journald; `orchestrator logs` follows them. The launchd equivalent is a follow-up.
- **Config.** `~/.config/orchestrator/config.toml`. Port, bind address, roots, default command, scrollback size.
- **Portability rule.** No Linux-only assumptions outside `internal/service/` and the PTY layer. Build must pass with `GOOS=darwin` even if untested.

### Protocol (v1)

One WebSocket per phone-host pair.

Text frames, JSON, `{ "t": "<type>", "rid": <request id>, ...payload }`. Replies echo `rid`; server-initiated messages have none.

| Client → host | Host → client |
|---|---|
| `hello {proto, device_id?, name, client_nonce}` | `hello {proto, host, version, server_nonce, fingerprint, auth_needed}` |
| `pair {code, pubkey, name}` | `pair.ok {device_id}` |
| `auth {device_id, sig}` | `auth.ok {device_id}` / `auth.fail` (then close) |
| `session.list` | `session.list {sessions[]}` |
| `session.create {cwd, cmd?, args?, name?, cols, rows}` | `session.created {session}` |
| `session.resume {id, cols, rows}` | `session.created {session}` |
| `session.attach {id, cols, rows}` | `session.attached {session}` then replay frames |
| `session.detach {id}` | `ok` |
| `session.resize {id, cols, rows}` | `ok` |
| `session.kill {id, signal?}` | `ok` |
| `session.rename {id, name}` | `ok` |
| `session.remove {id}` | `ok` |
| | `session.event {session}` on any change |
| | `session.removed {id}` |
| | `session.detached {id, reason}` (exited, slow) |
| `fs.list {path, hidden?}` | `fs.list {path, parent, entries[]}` |
| `fs.search {root?, query, limit?}` | `fs.search {entries[]}` |
| `fs.recents` | `fs.recents {paths[]}` |
| `claude.conversations {cwd}` | `claude.conversations {conversations[]}` |
| `host.info` | `host.info {host, version, fingerprint, port, addrs[], roots[], default_cmd, home}` |
| `ping` | `pong` |

`sig` is an Ed25519 signature over `"orch-auth-v1" || server_nonce || client_nonce || fingerprint || device_id`. The server nonce is single-use.

Binary frames: `[1 byte kind][4 byte session handle BE][payload]`. Kind `0x01` input (client → host), `0x02` output (host → client). The handle is per daemon run; the stable session id is the UUID in `session.list`. Multiple attached sessions share the socket.

Errors: `{ "t": "error", "rid": N, "code": "bad_request|unauthorized|forbidden|not_found|rate_limited|internal", "message": "..." }`. Three protocol errors close the connection.

### Dev tooling

- `daemon/web/` embedded xterm.js page served at `https://host:port/_debug/` when `--debug` is set. With `--debug`, connections from loopback are auto-authenticated. Never run `--debug` on a shared machine. Lets the daemon be built and tested fully before the app exists.
- `go test` coverage for ring buffer, protocol framing, auth handshake, session lifecycle, filesystem API, Claude integration, and a WebSocket integration test that pairs, creates, attaches, types, kills, and resumes.
- Linux CI job (`.github/workflows/daemon.yml`): gofmt, `go vet`, `go test -race`, and cross-compile checks for `darwin/arm64` and `linux/arm64`.

## Phone side (iOS app)

### Screens

1. **Hosts.** List of paired hosts with online dot and running/waiting counts. "+" opens the QR scanner. Tap a host to enter it.
2. **Sessions** (per host). Cards sorted: waiting first, then running by last activity, then exited/stale. Card shows name, folder (shortened), status, and the last line of output. Swipe left: kill. Long-press: rename. Pull to refresh. "+" starts the new-session flow.
3. **New session.** Folder picker: Recents (last 10 folders used on this host), then a breadcrumb browser with `.git` markers and a search field. Choosing a folder opens a bottom sheet: command (default `claude`), optional "resume previous conversation" list, name. One button: Start.
4. **Terminal.** Full screen `xterm`. Key bar above the keyboard: `Esc`, `Tab`, `Ctrl`, `↑`, `↓`, `←`, `→`, `/`, `⇧Tab`, paste. `Ctrl` is sticky for the next key. Pinch to zoom font. Long-press to select and copy. Back gesture detaches, session keeps running. Horizontal swipe on the top bar switches to the next session on the same host.
5. **Settings.** Font size, theme, key bar order, haptics. Per host: rename, forget.

### Must have

- **Pairing** by QR, fallback manual entry of address, port, code, fingerprint.
- **Connection manager.** One socket per host. On connect, try addresses in order: LAN, then Tailscale, then others. Exponential backoff on failure. Reconnect on app foreground and network change. Re-attach open terminals and replay automatically.
- **Key storage** in the iOS Keychain.
- **Terminal correctness.** 256 colors, true color, cursor styles, alternate screen, bracketed paste. Resize sent on rotation and keyboard show/hide.
- **Attention.** In-app badge on the host and session card when status is *waiting*. Local notification when the app is in the background and the socket is still alive. That is all for the MVP.
- **Stale session resume.** Card shows "Stale" with a Resume button that creates a new session in the same cwd running `claude --continue`.
- **Free-account constraints respected.** No push entitlement, no App Groups, no iCloud. Bundle ID and signing left to the sideloading tool.

### Nice to have if cheap

- Favorites in the folder picker.
- Font choice with a bundled Nerd Font for correct box drawing and icons.
- iPad layout with sessions list beside the terminal.

## Building and installing the iOS app without a Mac

The developer machine is Ubuntu. Flutter cannot compile for iOS on Linux, so compilation happens on a GitHub-hosted macOS runner and signing happens on Ubuntu with a free Apple ID.

**Build (GitHub Actions).** Workflow `.github/workflows/ios.yml`, triggered on tags `app-v*` and manually:

1. `macos-latest` runner, checkout, install Flutter (`subosito/flutter-action`, pinned version).
2. `flutter pub get`, `flutter build ios --release --no-codesign`.
3. Package `build/ios/iphoneos/Runner.app` into `Payload/` and zip it as `Orchestrator-unsigned.ipa`.
4. Upload as a workflow artifact and attach to a GitHub release.

The repo is private, so macOS minutes count 10x against the free plan's 2,000 minutes, roughly 200 real minutes a month. A build is about 8 minutes, so trigger on tags only, not on every push. Making the repo public removes the cap.

**Sign and install (Ubuntu).**

1. On the iPhone: Settings → Privacy & Security → Developer Mode → on.
2. Install `usbmuxd` and `libimobiledevice-utils`; plug in the phone; trust the computer.
3. Install **iloader** (Linux x86_64 release). Sign in with a spare Apple ID created for signing, complete 2FA.
4. Use iloader to install **SideStore** once and generate its pairing file with `idevice_pair`. SideStore refreshes sideloaded apps on the phone every few days so the 7-day certificate does not lapse.
5. Download the unsigned IPA from the release and install it through iloader (or directly through SideStore on the phone). Repeat for every new build.

Fallback if iloader breaks: **Sideloader** (Dadoum) CLI does the same signing and install from Linux.

**Known limits of this path.** Certificate lasts 7 days (SideStore refreshes it), at most 3 sideloaded apps, no push notifications, no TestFlight. Both signing tools depend on Apple's private developer endpoints and can break after Apple-side changes.

## Installation story (MVP)

**Host (Ubuntu)**

```
curl -fsSL https://raw.githubusercontent.com/markusbug/Orchestrator/main/scripts/install.sh | sh
orchestrator install
orchestrator pair
```

The install script downloads the latest release binary for `linux/amd64` or `linux/arm64` into `~/.local/bin`. If `ufw` is active with the default deny-incoming policy, the port must be opened (`sudo ufw allow 7391/tcp`); `orchestrator status` and the install script should print this hint when they detect an active firewall. Until it exists, `make build` in the repo produces `bin/orchestrator`. The phone must be on the same network as the host; the pair output prints every address it found.

**Phone (iPhone)**

Unsigned IPA from GitHub releases, installed via iloader/SideStore as described above.

## Acceptance checklist

- [x] Start a Claude Code session from the phone in a chosen folder in 3 taps from the host screen. (Verified on the iPhone 2026-09-05.)
- [ ] Close the app, wait 10 minutes, reopen, and the session is still running with its screen restored.
- [ ] Turn Wi-Fi off and on again on the phone; app reconnects within 10 seconds without user action.
- [ ] Run 10 sessions concurrently on the host; list and switching remain responsive.
- [ ] Claude asks a permission question; the session card shows *waiting* within 2 seconds.
- [ ] Kill and restart the daemon; sessions show as stale; Resume starts `claude --continue` in the right folder.
- [ ] Revoke a phone from the CLI; that phone is disconnected and cannot reconnect.
- [ ] Daemon survives logout and reboot on Ubuntu (linger enabled) and comes back with stale sessions listed.
- [x] A tagged commit produces an installable unsigned IPA from GitHub Actions in under 10 macOS minutes. (app-v0.1.0 to v0.1.2: about 4 minutes each; installed with iloader.)
- [x] `GOOS=darwin go build ./...` succeeds even though macOS is not yet tested. (Checked in CI on every daemon change.)

## Build order

1. ✅ Daemon: session manager, ring buffer, WebSocket protocol, debug web page. Verify with a browser on Ubuntu.
2. ✅ Daemon: TLS, pairing, auth, fs API, systemd install, CLI. Linux CI with tests and darwin cross-compile check.
3. ✅ Repo: Flutter project skeleton, iOS workflow producing an unsigned IPA. (Sideloading still to do.)
4. ✅ App: pairing, connection manager, hosts, sessions list, terminal view.
5. ✅ App: folder picker, new session flow, settings.
6. ✅ Daemon: hooks-based attention, conversation list, stale resume. ✅ App: badges, local notifications, resume button.
7. Release: goreleaser for the daemon, install script, tagged app builds.
8. After MVP: macOS host (launchd), Android build, then the rest of PLAN.md.
