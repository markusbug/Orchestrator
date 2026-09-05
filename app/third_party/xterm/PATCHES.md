# Vendored xterm.dart 4.0.0

Copied from pub.dev (`xterm` 4.0.0, BSD-3 licence, see LICENSE) with one change:

- `TerminalView` gained a `textInputAction` parameter, forwarded to the
  internal text input client, and `send`/`go` actions are treated like `done`
  (a carriage return). Upstream hardcodes `TextInputAction.newline`, which on
  iOS makes the return key insert a line feed; Claude Code reads a line feed
  as "insert newline", so there was no way to submit a prompt from the soft
  keyboard.

Only `lib/`, `pubspec.yaml`, `LICENSE`, `README.md`, and `CHANGELOG.md` are
kept. Drop this directory and switch `pubspec.yaml` back to the pub.dev
package once upstream exposes the input action.
