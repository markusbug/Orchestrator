# Desktop app

How the desktop app is put together, how to build and release it, and what is
deliberately left out. User-facing install steps are in [../README.md](../README.md).

## What it is

A Flutter app in the same project as the phone app (`app/`), with `linux/` and
`macos/` targets and its own entry point, `app/lib/main_desktop.dart`. Build it
with `-t`:

```
cd app
flutter run   -d linux   -t lib/main_desktop.dart      # or: make desktop-run
flutter build linux --release -t lib/main_desktop.dart
flutter build macos --release -t lib/main_desktop.dart
```

The phone app's `main.dart` boots a client — keychain, notifications, the list
of paired hosts — none of which the desktop app wants, since it controls the
daemon on *this* machine. Sharing one `main()` would have been a pile of
`Platform.isLinux` conditionals, so there are two entry points instead. They
share `app/lib/theme.dart`, which holds the terracotta seed colour, and nothing
else: `lib/desktop/` imports nothing from `lib/net/`, `lib/services/` or
`lib/ui/`.

Window contents are deliberately small — status, a pairing QR with its 6-digit
fallback, the paired devices, and one switch. No terminal view, no log viewer,
no settings panel. Anything more is the phone app's job or the CLI's.

## How it talks to the daemon

By running the `orchestrator` binary and parsing JSON, not by speaking HTTP over
the admin unix socket.

The socket exists and is a perfectly good API, but its path comes out of
`config.DefaultPaths`, which has an `$XDG_RUNTIME_DIR` branch on Linux, a
different one on macOS, and an `os.TempDir()/orchestrator-<uid>-<fnv32>.sock`
fallback when the path exceeds 96 bytes. Reimplementing that in Dart means two
copies of a rule that will drift, and a third when Windows needs named pipes.
Shelling out keeps one implementation, in Go, and costs about 15 ms per call.

The commands it uses:

| Command | For |
|---|---|
| `orchestrator status --json` | the status card |
| `orchestrator devices --json` | the devices list |
| `orchestrator devices revoke <id> --json` | the revoke button |
| `orchestrator pair --json` | the QR (the `uri` field is what the QR encodes) |
| `orchestrator install --json` | the setup button |
| `orchestrator _service` | installed / enabled / running |
| `orchestrator _service enable\|disable\|start\|stop` | the start-at-login switch, and Start / Shut down |
| `orchestrator _paths` | where state lives |

The `_`-prefixed ones are internal. The admin API has no event stream, so the
app polls: every 2 s with the window open, every 20 s when it is only a tray
icon.

`_service stop` is not just `systemctl stop`: a daemon started by hand is not
one the service manager owns, so it falls back to SIGTERM on the pid from
`status` and waits for the admin socket to go quiet. Shutting down has to work
whichever way the daemon was started, or it is not a shutdown.

## Where the daemon binary lives

The app ships a copy of the daemon and **copies it to a stable path before
installing the service**:

| | bundled | stable |
|---|---|---|
| Linux `.deb` | `/usr/lib/orchestrator/orchestrator` | already stable, used as-is |
| Linux AppImage | `/tmp/.mount_XXXX/usr/lib/orchestrator/orchestrator` | `~/.local/bin/orchestrator` |
| macOS | `Orchestrator.app/Contents/Resources/orchestrator` | `~/Library/Application Support/orchestrator/bin/orchestrator` |

This is not tidiness. Both the service file and `claude-hooks.json` record the
daemon's **absolute path** (`core.Open` bakes `os.Executable()` into the hooks
file), and an AppImage's mount point lasts exactly one run while a `.app` can be
dragged anywhere. A service installed from such a path breaks on next launch.

Paths under `/usr`, `/opt` or `/Applications` are taken as already stable and
used directly, so a `.deb` install does not end up with two binaries drifting
apart. The copy goes to a sibling file and is then renamed, because overwriting
a running binary fails with `ETXTBSY`; the running daemon keeps the old inode
and picks the new one up when it next restarts, which avoids interrupting live
sessions.

## Start at login

One switch, "Start Orchestrator when I log in", default on. It flips two things:

- **the daemon service** — `orchestrator _service enable|disable`, which is
  `systemctl --user enable/disable` or `launchctl enable/disable`
- **the app's own login item** — `~/.config/autostart/orchestrator.desktop` or
  `~/Library/LaunchAgents/io.freedomfactory.orchestrator.app.plist`, written
  directly by `lib/desktop/autostart.dart`

Turning it **off never stops a running daemon**. `orchestrator uninstall` would
(`systemctl --user disable --now`), which is why the switch calls
`_service disable` instead — opting out of autostart must not kill live
sessions.

Tray **Quit** exits the app only. That is the whole promise of the product:
sessions outlive the phone, and they outlive the desktop UI too. Removing the
daemon entirely is `orchestrator uninstall`.

The login item is written by hand rather than with `launch_at_startup`, whose
macOS path drags in the LaunchAtLogin Swift package and an Xcode/SPM setup step
for what is one small file on each of the two platforms.

## Platform notes

**macOS App Sandbox is off.** The Flutter template turns it on. Inside the
sandbox the app cannot exec the bundled daemon, cannot write
`~/Library/LaunchAgents`, and cannot run `launchctl` — and it fails at runtime,
not at build time. Both `Runner/*.entitlements` files have the key removed;
do not let a `flutter create` put it back.

**macOS LaunchAgents die at logout.** A user agent runs only while the user is
logged in, and there is no `loginctl enable-linger` equivalent. Closing the lid
is fine; logging out ends your sessions. A `LaunchDaemon` would survive but
needs root and would run `claude` as root, which is worse.

**The Linux GUI binary is `orchestrator-desktop`**, set via `BINARY_NAME` in
`app/linux/CMakeLists.txt`, because the daemon ships in the same directory.

**Tray on Linux is not guaranteed.** GNOME has no StatusNotifier host without
the AppIndicator extension (Ubuntu ships and enables it). The app probes DBus
for `org.kde.StatusNotifierWatcher` at startup; when nothing answers, closing
the window quits rather than hiding, so the app can never become invisible with
no way back. `setToolTip` also throws on Linux app indicators and is skipped
there.

**Wayland ignores window positioning.** Set the size, do not try to place the
window.

## Building the packages

```
make desktop-package      # .deb + AppImage into dist/
```

Needs `nfpm` (`go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest`) and
`appimagetool` on `PATH`, plus the Linux build deps: `clang cmake ninja-build
pkg-config libgtk-3-dev liblzma-dev libayatana-appindicator3-dev
libsecret-1-dev`.

`packaging/build-linux.sh` assembles `packaging/out/root`, calls `nfpm` for the
`.deb`, then builds an AppDir and calls `appimagetool`. The AppImage bundles the
appindicator and libsecret shared libraries a stock desktop may lack; a library
that is missing on the build machine is skipped with a note rather than being
fatal.

`nfpm` was chosen over `flutter_distributor` (which has been discontinued in
favour of fastforge) and over hand-rolled `dpkg-deb`: it is a single Go binary
with a declarative manifest, which suits a repo that already writes its install
tooling by hand.

There is deliberately **no `postinst`**. The daemon is a systemd *user* service,
which belongs to a user rather than to `dpkg`, so the app enables it on first
launch.

## Releasing

Tag `desktop-v*`. `.github/workflows/desktop.yml` runs two jobs:

- **linux** on `ubuntu-22.04` — pinned rather than `-latest` so the glibc the
  artifacts need is old enough for 22.04 *and* 24.04. Cross-builds the daemon
  for `linux/{amd64,arm64}`, builds the Flutter bundle, produces the `.deb` and
  the AppImage. The desktop app itself is amd64 only; arm64 users get the
  standalone daemon binary through `scripts/install.sh --headless`.
- **macos** on `macos-14` — cross-builds the daemon for `darwin/{arm64,amd64}`
  and `lipo`s them into one universal CLI, builds the app, copies the daemon
  into `Contents/Resources/`, then **re-signs ad-hoc**. That last step matters:
  adding a file invalidates Flutter's signature and an Apple-silicon app with a
  broken signature will not launch at all. It is not notarization — Gatekeeper
  still warns, see the README.

The daemon builds `CGO_ENABLED=0` everywhere; `creack/pty` is pure syscalls and
`modernc.org/sqlite` is pure Go, so cross-compilation needs no toolchain.

Tag series in this repo: `v*` daemon, `relay-v*` relay, `app-v*` phone app,
`desktop-v*` desktop app.

## Windows, later

The blockers are in the daemon, not the app:

- no ConPTY — `creack/pty` is Unix-only
- `config.DefaultPaths` has no `%APPDATA%` branch
- the admin socket is AF_UNIX with `os.Getuid()`; Go supports AF_UNIX on
  Windows, so measure before reaching for named pipes
- `service_windows.go` against Task Scheduler or a Windows service

On the Flutter side `flutter create --platforms=windows` plus the same
`tray_manager`/`window_manager` pair works unchanged, and because the app shells
out to the CLI rather than opening the socket itself, none of the Dart code has
to change. Packaging would be Inno Setup or MSIX, and SmartScreen is the same
kind of signing decision as Gatekeeper.
