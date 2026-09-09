# Orchestrator — High-Level Plan

Spawn and keep Claude Code sessions running on your own machine, and drive them from your phone.

## 1. Product summary

- **Host daemon** runs on a laptop/desktop (Linux, macOS, Windows). It owns terminal sessions (PTYs) and keeps them alive independently of any client.
- **Desktop app** is a small tray/menu-bar companion that installs and supervises the daemon, shows a pairing QR code, and lets the user manage devices and sessions. Install is a native package: `.deb`/`.rpm`/AppImage, `.dmg`, Windows installer.
- **Mobile app** (iOS + Android) pairs with one or more hosts, browses the host filesystem, spawns a Claude Code session in any folder, attaches a full terminal to it, detaches, and re-attaches later. Any number of sessions.
- **Nothing depends on the phone staying open.** All state lives on the host. The phone is a remote control.

Design principle: the daemon is a generic "remote persistent terminal" service. Claude Code is the default command, but any CLI works. That keeps the core small and makes Claude Code integrations (resume, attention detection) additive layers.

## 2. Architecture

```
┌─────────────── Host machine ───────────────┐        ┌──────── Phone ────────┐
│                                            │        │                       │
│  Desktop app (Flutter, tray)               │        │  Mobile app (Flutter) │
│    setup wizard · QR pairing · devices     │        │    hosts · sessions   │
│    session overview · settings · updates   │        │    folder picker      │
│        │ localhost (same protocol)         │        │    terminal (xterm)   │
│        ▼                                   │  WSS   │                       │
│  orchestrator daemon (Go, user service)  ◄─┼────────┼──►  connection mgr    │
│    ├─ session manager (PTY per session)    │  LAN / │                       │
│    ├─ scrollback ring buffer per session   │  VPN / └───────────────────────┘
│    ├─ fs browse API (allow-listed roots)   │  relay (later)
│    ├─ auth: paired devices, TLS pinning    │
│    ├─ metadata store (SQLite)              │
│    └─ Claude Code hooks endpoint           │
│              │ spawns                      │
│              ▼                             │
│     claude (in /path/to/project)  × N      │
└────────────────────────────────────────────┘
```

### 2.1 Tech choices

| Piece | Choice | Why |
|---|---|---|
| Daemon | **Go** | Single static binary, trivial cross-compilation, mature PTY libs (`creack/pty`, ConPTY on Windows), service install via `kardianos/service`, pure-Go SQLite (`modernc.org/sqlite`). Ideal for a long-running headless process. |
| Desktop app | **Flutter desktop** | Same language/UI kit as the mobile app: shared protocol client, models, theme, and even the terminal widget. Tray via `tray_manager`, autostart via `launch_at_startup`, native installers via `flutter_distributor`. |
| Mobile app | **Flutter** | One codebase for iOS + Android. `xterm` (Dart) is a real terminal emulator with native rendering, avoiding WebView keyboard pain. |
| Transport | **WebSocket over TLS**, one connection per host, multiplexed | Works over LAN, VPN, and through a future relay unchanged. Mobile-friendly (one socket, one auth). |
| Wire format | JSON control messages + binary PTY frames | Simple, debuggable, efficient for terminal bytes. Versioned. |
| Auth | QR pairing → per-device key, self-signed TLS cert pinned at pairing | No CA, no accounts, no cloud. Secure by default. |
| Packaging | goreleaser (daemon) + flutter_distributor (apps) + GitHub Actions | Reproducible installers for every platform on each tag. |

Alternative considered: TypeScript daemon (`node-pty`). Rejected for distribution weight and runtime dependency; Go gives a 10 MB binary with no runtime. Alternative for desktop UI: Tauri/Electron. Rejected to avoid a third stack; Flutter desktop reuses the mobile code.

### 2.2 Daemon (Go)

Responsibilities:

- **Session manager.** `create(cwd, cmd, args, env, cols, rows)` spawns a PTY (Unix: `creack/pty`; Windows: ConPTY). Each session has an id, name, cwd, command, pid, status, exit code, timestamps, and an *attention* flag. No limit on count; a configurable resource guard warns beyond N.
- **Scrollback.** Per-session ring buffer of raw output (default 2 MB). On attach the daemon replays the buffer then sends a resize to force the TUI to redraw cleanly. Multiple clients can attach to the same session simultaneously.
- **Persistence.** Session metadata in SQLite. On daemon restart, sessions whose processes died are shown as *stale* with a one-tap **Resume** that re-launches `claude --continue` in the same folder. (Milestone 5 moves PTY ownership into per-session host processes so sessions survive daemon upgrades too.)
- **Filesystem API.** List directory (dirs first, hidden toggle, git-repo marker), stat, bounded name search, favorites, recents. Scoped to allow-listed roots (default: home directory).
- **Claude Code awareness.** Sessions launched as `claude` get `--settings <generated file>` adding `Notification` and `Stop` hooks that call back into the daemon over a local socket. This sets/clears the *attention* flag reliably ("Claude is waiting for you", "Claude finished"). Terminal bell (`\a`) is used as a fallback heuristic. The daemon can also list past conversations from `~/.claude/projects/` so the phone can offer *resume* of any earlier session in a folder.
- **Auth.** Paired devices table (public key, name, created, last seen). Pairing is a one-time code embedded in a QR (host, port, cert fingerprint, code). Revocation via desktop app or CLI.
- **Local admin API.** Unix socket / named pipe for the desktop app and CLI (no auth needed, filesystem permissions).
- **CLI.** `orchestrator serve | install | uninstall | status | pair | devices | sessions | logs`. The desktop app is a GUI over the same operations; power users and headless servers can use the CLI alone.
- **Service install.** systemd user unit (Linux), LaunchAgent (macOS), Scheduled Task at logon or Windows Service (Windows). Auto-restart on crash. Logs to a rotating file.

### 2.3 Protocol

Single WebSocket per (client, host). After TLS + auth handshake:

- **Control (JSON, text frames):** `hello`, `auth`, `session.list`, `session.create`, `session.attach`, `session.detach`, `session.resize`, `session.signal`, `session.kill`, `session.rename`, `session.event` (server push: status/attention changes), `fs.list`, `fs.stat`, `fs.search`, `host.info`, `claude.conversations`.
- **Data (binary frames):** `[1 byte kind][4 byte session id][payload]` for PTY input (client→host) and PTY output (host→client).
- Protocol version negotiated in `hello`; documented in `docs/protocol.md`.

### 2.4 Connectivity

1. **LAN (M1).** Phone connects directly to the host IP/port from the QR. mDNS advertisement (`_orchestrator._tcp`) so the app can rediscover a host whose IP changed.
2. **Remote via VPN (M1).** Tailscale/WireGuard/ZeroTier make the host reachable anywhere with zero Orchestrator infrastructure. The desktop app detects Tailscale and shows the stable tailnet address in the QR. Documented as the recommended remote path.
3. **Relay (done 2026-09-06).** Design in [docs/RELAY.md](docs/RELAY.md). Hosted at `relay.markushaas.com` and on by default; self-hosting runs the same binary. The daemon opens an outbound WSS, the phone connects to the relay by host id over TLS routed by SNI, and the relay forwards opaque bytes. End-to-end encrypted with the pairing keys so the relay is a dumb pipe. Also the natural place to fan out push notifications.

**Zero network configuration is a product requirement.** A user must never open a port, edit a firewall, or read an IP address to use Orchestrator. The first phone pairing on the developer's laptop failed because `ufw` silently dropped the daemon's port; that is exactly the class of problem end users will not diagnose. The path out of it:

- **Short term (MVP):** the installer and `orchestrator status` detect an active host firewall (`ufw`, `firewalld`, Windows Defender Firewall, macOS application firewall) and either add the allow rule during `orchestrator install` (with a clear prompt) or print the exact command. The app's pairing error names the firewall as the likely cause.
- **Real fix:** the daemon opens an *outbound* connection, so nothing listens on the host and no inbound rule is ever needed. That is the relay in item 3, and it is the connection path the app takes by default. LAN direct becomes a fallback optimisation, not something the user has to make work.
- **Public API:** the relay is fronted by a documented API (host registration, device pairing, session events, opaque frame forwarding) so that the phone app, the desktop app, and third-party integrations all use the same contract. Self-hosting the relay stays possible; the hosted one is what makes it work out of the box.

### 2.5 Security model

- The daemon runs as the logged-in user with that user's full privileges. Anyone holding a device key has the user's shell. Therefore: keys live in Keychain/Keystore, pairing codes expire in 5 minutes and are single-use, failed auth is rate-limited, every device is listed and revocable.
- Self-signed TLS generated on first run; the phone pins the fingerprint from the QR. No plaintext listener except the local admin socket.
- Bind address configurable: all interfaces (default), LAN only, or Tailscale interface only.
- Filesystem browsing limited to allow-listed roots. Session commands are free-form by design (it is a terminal), but the UI defaults to `claude`.

## 3. Desktop app (setup & control)

Goal: a first-time user goes from download to "phone connected" in under two minutes without reading docs.

- **Install.** Native package per OS (see §5). The package contains the daemon binary and the desktop app. Nothing else to install.
- **First launch wizard.**
  1. Welcome → "Start Orchestrator in the background" (installs and starts the user service; asks for permissions if the OS needs them).
  2. "Get the phone app" (App Store / Play Store links + QR to store).
  3. Big pairing QR + 6-digit code fallback. Detects Tailscale and explains LAN vs anywhere. Shows "Phone connected ✓" live.
  4. Done. App minimizes to tray / menu bar.
- **Tray menu.** Status dot, open dashboard, pair a device, list of running sessions (open one in the desktop terminal view), start/stop service, quit.
- **Dashboard window.** Sessions (attach, rename, kill, resume), Devices (rename, revoke), Settings (allowed folders, default command, bind interface, port, scrollback size, autostart, logs), About/Update.
- **Updates.** Checks GitHub releases; offers download of the new installer (or `brew upgrade` / `apt` hints where applicable). In-place auto-update is a later polish item.

## 4. Mobile app (UX)

Guiding rule: **three taps from launch to a running Claude Code session in the right folder.**

- **Hosts.** List of paired machines with online/offline and session counts. Add host by scanning QR. Auto-reconnect with backoff; offline hosts stay listed.
- **Sessions.** Cards per session: name, folder, status (running / waiting for you / finished / stale), last activity, a two-line preview of the latest output. Attention sessions bubble to the top with a badge. Swipe to kill or rename. Pull to refresh.
- **New session.** Folder picker with Recents and Favorites first, then a breadcrumb browser with git-repo markers and search. Then a compact sheet: command (Claude Code default; recent conversations to resume are listed right there), name, "Open when created". One tap starts it.
- **Terminal.** Full-screen `xterm` view. Persistent key bar: Esc, Tab, Ctrl, ↑↓←→, `/`, Shift+Tab (Claude Code mode cycle), paste. Long-press for selection/copy. Pinch to change font size. Landscape support. Horizontal swipe to move between sessions. Background/foreground transitions re-attach and replay transparently.
- **Attention.** While the app is open: in-app banner and badge. Background: local notification when the socket is alive; system push arrives with the relay milestone. Bridge in the meantime: daemon can forward attention events to a user-configured webhook (ntfy, Pushover, Telegram).
- **Settings.** Theme, font, key-bar layout, haptics, per-host defaults.
- **Platform notes.** iOS suspends sockets in the background within seconds; the design assumes disconnect/replay is normal. Secrets in `flutter_secure_storage`. QR via `mobile_scanner`.

Later, optional **structured mode**: run Claude Code through its stream-JSON output and render a native chat view with tool cards and native permission buttons. The terminal remains the source of truth; this is a nicer skin, not a replacement.

## 5. Distribution & installers

| Platform | Artifact | Contents / install behavior |
|---|---|---|
| Linux | `.deb`, `.rpm`, AppImage; later Flatpak, AUR | Daemon + desktop app + `.desktop` entry. First app launch enables the systemd user unit (avoids root-in-postinst). |
| macOS | `.dmg` (universal binary) + Homebrew cask; `brew` formula for the daemon alone | `Orchestrator.app` bundles the daemon; first launch installs a LaunchAgent. Requires Apple Developer ID signing + notarization or Gatekeeper blocks it. |
| Windows | Installer `.exe` (Inno Setup) and `.msix`; `winget` manifest | Installs to Program Files, registers autostart task, adds firewall rule prompt. Code-signing certificate strongly recommended to avoid SmartScreen warnings. |
| iOS | App Store, TestFlight for beta | Apple Developer Program required. |
| Android | Google Play, plus direct `.apk` on GitHub releases | Play Console account required. |
| Headless / servers | Static daemon binary + `curl \| sh` installer | `orchestrator install && orchestrator pair` prints the QR in the terminal. |

CI: GitHub Actions matrix (ubuntu, macos, windows) builds daemon with goreleaser and apps with flutter_distributor on every tag; drafts a GitHub release with all artifacts and checksums.

## 6. Repository layout

```
Orchestrator/
  PLAN.md                 this document
  README.md
  docs/                   architecture, protocol, security, packaging notes
  daemon/                 Go module
    cmd/orchestrator/     CLI entry point
    internal/session/     PTY lifecycle, ring buffer, hooks
    internal/api/         WebSocket server, protocol handlers
    internal/auth/        pairing, device keys, TLS
    internal/fsapi/       filesystem browsing
    internal/store/       SQLite metadata
    internal/service/     OS service install
    web/                  embedded debug client (xterm.js) for development
  app/                    Flutter project (mobile + desktop targets)
    lib/core/             protocol client, models, connection manager
    lib/features/         hosts, sessions, folder picker, terminal, setup wizard
    lib/desktop/          tray, wizard, dashboard (desktop-only)
  packaging/              installer configs (nfpm, Inno Setup, dmg, launchd/systemd units)
  .github/workflows/      CI and release pipelines
```

## 7. Milestones

**M0 — Foundations (this commit onward).** Repo, plan, protocol draft, CI skeleton.

**M1 — Daemon core.** Spawn/attach/detach/resize/kill sessions over WebSocket; ring-buffer replay; embedded xterm.js debug page so everything can be tested from a browser before any app exists; `orchestrator serve`. *Done when: a Claude Code session started from the browser keeps running after the tab closes and re-attaches with correct screen state.*

**M2 — Auth, pairing, filesystem.** Self-signed TLS, QR pairing, device store, fs API, `install`/`pair`/`devices` CLI, service units for all three OSes.

**M3 — Mobile app MVP.** Pairing, hosts, session list, folder picker, terminal with key bar, new-session flow. LAN and Tailscale. TestFlight / internal Play track.

**M4 — Desktop app + installers.** *Done for Linux and macOS.* Flutter desktop app with a status/pairing/devices window, a tray icon, close-to-tray and a start-at-login switch; `.deb`, AppImage and `.dmg` built on `desktop-v*` tags, plus a `curl | sh` installer. Deliberately trimmed from the spec below: no terminal view, no log viewer, no settings panel. Still open: signing and notarization (the `.dmg` is unsigned), Windows, and a Linux arm64 desktop build. See [docs/DESKTOP.md](docs/DESKTOP.md).

**M5 — Robustness.** Claude Code hooks → attention flag; stale-session resume via `claude --continue`; conversation list from `~/.claude/projects`; per-session host processes so daemon upgrades don't kill sessions; webhook notifications; mDNS rediscovery.

**M6 — Remote & notifications.** Relay service (self-hostable, E2E encrypted) — done, hosted at `relay.markushaas.com`; APNs/FCM push for attention events; multi-host polish; iPad and desktop terminal layouts; auto-update.

## 8. Risks and mitigations

- **Terminal width on phones.** Claude Code's TUI wants ~80 columns; a portrait phone at a readable font gives ~45. Mitigate with a compact default font, pinch-zoom, landscape mode, and the daemon reporting the phone's size so Ink lays out for it. Structured mode is the long-term answer.
- **Daemon restarts kill sessions.** Mitigated by `claude --continue` resume in M5, and structurally by per-session host processes.
- **Remote reachability.** The hosted relay is the default no-setup path and the one the app dials first on every network; LAN and Tailscale remain as faster direct fallbacks.
- **Signing costs and friction.** Apple Developer ID and a Windows code-signing certificate are needed for a smooth install. Budget for them before M4.
- **Windows ConPTY quirks.** Test early (M1) on real Windows; keep Windows shell defaults sane.
- **Security exposure.** A paired phone is a shell on the laptop. Pairing must be short-lived, keys hardware-backed on the phone, and revocation one tap away.

## 9. Open decisions

- Whether the hosted relay (free today) stays free, or becomes donation-funded or paid once it carries more than a handful of hosts.
- Whether the desktop app should also be the CLI's host for sessions on the local screen (attach to a session in a desktop window), or stay a pure control panel. Plan assumes it can attach, since the terminal widget is shared.
- Minimum OS versions (proposal: Ubuntu 22.04+, macOS 13+, Windows 10 1903+ for ConPTY, iOS 16+, Android 8+).
