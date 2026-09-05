# Orchestrator MVP

Target user: someone who already runs Claude Code daily and wants to start, watch, and nudge sessions on their own machine from their phone. They are comfortable with a terminal and can install Tailscale.

The MVP is done when this works end to end:

> Install the daemon on a laptop with one command. Pair the phone by scanning a QR code printed in the terminal. From the phone, browse to a project folder, start Claude Code there, give it a task, lock the phone, come back an hour later on a different network, and pick up the same session with its screen intact.

## Scope

**In**

- Host daemon (Go) as a CLI, running as a user service.
- Mobile app (Flutter) for iOS and Android.
- Direct connection over LAN and over Tailscale. Nothing else.
- Any number of sessions per host, any number of hosts per phone.

**Out (explicitly, until after MVP)**

- Desktop GUI, tray app, setup wizard.
- Native installers (.deb, .dmg, .exe). Release binaries, a Homebrew formula, and a `curl | sh` script are enough.
- Relay server, accounts, billing, push notifications.
- Windows host support. Design for it, ship it after Linux and macOS work.
- Structured/chat rendering of Claude Code. Terminal only.
- Session survival across daemon restarts. Stale sessions are marked and can be resumed with one tap via `claude --continue`.

## Host side (daemon)

### Commands

```
orchestrator serve              run in foreground (used by the service)
orchestrator install            register user service (systemd --user / launchd) and start it
orchestrator uninstall
orchestrator status             running? port? address(es)? paired devices? session count?
orchestrator pair               print QR + code in terminal, valid 5 minutes
orchestrator devices [revoke]   list or revoke paired phones
orchestrator sessions [kill]    list or kill sessions
orchestrator logs               tail the log file
```

### Must have

- **Sessions.** Spawn a PTY with cwd, command, args, cols, rows. Default command is `claude`. Track id, name, cwd, pid, status (running, waiting, exited, stale), exit code, created, last output time.
- **Scrollback.** Per-session ring buffer, 1 MB default. On attach: replay buffer, then apply the client's size so the TUI redraws.
- **Multi-attach.** More than one client on the same session at once. Last resize wins.
- **Metadata store.** SQLite file under the config dir. On startup, sessions whose pid is gone become *stale* and keep their cwd for resume.
- **Filesystem API.** List a directory (dirs first, hidden toggle, `.git` marker), stat, and a bounded recursive name search. Roots default to the home directory.
- **Claude conversation list.** Read `~/.claude/projects/<encoded cwd>/*.jsonl` to list previous conversations for a folder with their first prompt and timestamp, so the phone can offer *resume* when creating a session.
- **Attention flag.** Launch `claude` with `--settings <generated json>` that adds `Notification` and `Stop` hooks calling `orchestrator _hook <session id> <event>` over the local admin socket. Sets status to *waiting* or clears it. Fallback: terminal bell.
- **Transport.** WebSocket over TLS on a configurable port (default 7391). Self-signed cert generated on first run, fingerprint printed by `status` and embedded in the QR.
- **Auth.** Pairing: QR carries `{addresses, port, fingerprint, code}`. Phone connects, presents code and its public key, daemon stores it. Later connections: challenge signed by the phone's key. Rate limit failures. Codes are single-use.
- **Addresses in QR.** All non-loopback IPv4/IPv6 addresses, with Tailscale addresses marked as such so the app prefers them when off-LAN.
- **Local admin socket.** Unix socket for CLI subcommands and hooks. No auth beyond file permissions.
- **Service install.** systemd user unit on Linux, LaunchAgent on macOS. Restart on failure. Log file with rotation.
- **Config.** `~/.config/orchestrator/config.toml` (XDG) / `~/Library/Application Support/orchestrator/`. Port, bind address, roots, default command, scrollback size.

### Protocol (v1)

One WebSocket per phone-host pair.

Text frames, JSON, `{ "t": "<type>", "id": <req id>, ...}`:

| Client → host | Host → client |
|---|---|
| `hello {proto, device}` | `hello {proto, host, version}` |
| `auth {signature}` | `auth.ok` / `auth.fail` |
| `session.list` | `session.list {sessions[]}` |
| `session.create {cwd, cmd, args, name, cols, rows}` | `session.created {session}` |
| `session.attach {id, cols, rows}` | `session.attached {session}` then replay |
| `session.detach {id}` | |
| `session.resize {id, cols, rows}` | |
| `session.kill {id, signal}` | |
| `session.rename {id, name}` | |
| | `session.event {session}` on any status change |
| `fs.list {path, hidden}` | `fs.list {entries[]}` |
| `fs.search {root, query, limit}` | `fs.search {entries[]}` |
| `claude.conversations {cwd}` | `claude.conversations {items[]}` |
| `ping` | `pong` |

Binary frames: `[1 byte kind][4 byte session id BE][payload]`. Kind `0x01` input (client → host), `0x02` output (host → client). Multiple attached sessions share the socket.

Errors: `{ "t": "error", "id": <req id>, "code": "...", "message": "..." }`.

### Dev tooling

- `daemon/web/` embedded xterm.js page served at `https://host:port/_debug` when `--debug` is set. Lets the daemon be built and tested fully before the app exists.
- `go test` coverage for ring buffer, protocol framing, auth handshake, session lifecycle.

## Phone side (app)

### Screens

1. **Hosts.** List of paired hosts with online dot and running/waiting counts. "+" opens the QR scanner. Tap a host to enter it.
2. **Sessions** (per host). Cards sorted: waiting first, then running by last activity, then exited/stale. Card shows name, folder (shortened), status, and the last line of output. Swipe left: kill. Long-press: rename. Pull to refresh. "+" starts the new-session flow.
3. **New session.** Folder picker: Recents (last 10 folders used on this host), then a breadcrumb browser with `.git` markers and a search field. Choosing a folder opens a bottom sheet: command (default `claude`), optional "resume previous conversation" list, name. One button: Start.
4. **Terminal.** Full screen `xterm`. Key bar above the keyboard: `Esc`, `Tab`, `Ctrl`, `↑`, `↓`, `←`, `→`, `/`, `⇧Tab`, paste. `Ctrl` is sticky for the next key. Pinch to zoom font. Long-press to select and copy. Back gesture detaches, session keeps running. Horizontal swipe on the top bar switches to the next session on the same host.
5. **Settings.** Font size, theme, key bar order, haptics. Per host: rename, forget.

### Must have

- **Pairing** by QR, fallback manual entry of address, port, code, fingerprint.
- **Connection manager.** One socket per host. On connect, try addresses in order: LAN, then Tailscale, then others. Exponential backoff on failure. Reconnect on app foreground and network change. Re-attach open terminals and replay automatically.
- **Key storage** in Keychain / Keystore.
- **Terminal correctness.** 256 colors, true color, cursor styles, alternate screen, bracketed paste. Resize sent on rotation and keyboard show/hide.
- **Attention.** In-app badge on the host and session card when status is *waiting*. Local notification when the app is in the background and the socket is still alive. That is all for the MVP.
- **Stale session resume.** Card shows "Stale" with a Resume button that creates a new session in the same cwd running `claude --continue`.

### Nice to have if cheap

- Favorites in the folder picker.
- Font choice with a bundled Nerd Font for correct box drawing and icons.
- iPad layout with sessions list beside the terminal.

## Installation story (MVP)

**Host**

```
curl -fsSL https://orchestrator.dev/install.sh | sh   # or: brew install <tap>/orchestrator
orchestrator install
orchestrator pair
```

Tailscale is documented as the way to reach the laptop from outside the LAN. The pair output says whether a Tailscale address was found.

**Phone**

TestFlight and Play internal testing links. Public store listings after the MVP proves itself.

## Acceptance checklist

- [ ] Start a Claude Code session from the phone in a chosen folder in 3 taps from the host screen.
- [ ] Close the app, wait 10 minutes, reopen, and the session is still running with its screen restored.
- [ ] Switch phone from Wi-Fi to mobile data with Tailscale on; app reconnects within 10 seconds without user action.
- [ ] Run 10 sessions concurrently on the host; list and switching remain responsive.
- [ ] Claude asks a permission question; the session card shows *waiting* within 2 seconds.
- [ ] Kill and restart the daemon; sessions show as stale; Resume starts `claude --continue` in the right folder.
- [ ] Revoke a phone from the CLI; that phone is disconnected and cannot reconnect.
- [ ] Both Linux and macOS hosts pass the above.

## Build order

1. Daemon: session manager, ring buffer, WebSocket protocol, debug web page. Verify with a browser.
2. Daemon: TLS, pairing, auth, fs API, service install, CLI.
3. App: pairing, connection manager, hosts, sessions list, terminal view.
4. App: folder picker, new session flow, settings.
5. Daemon: hooks-based attention, conversation list, stale resume. App: badges, notifications, resume button.
6. Release scripts, install script, Homebrew tap, TestFlight and Play internal tracks.
