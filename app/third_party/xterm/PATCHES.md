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

Second change (scrolling under Claude Code's fullscreen renderer):

- The scrollback `Scrollable` inside `TerminalView` uses a physics that only
  accepts drags when the scrollback is non-empty. With the iOS default
  `BouncingScrollPhysics` it accepted every drag, so in the alternate screen
  buffer (empty scrollback) it won the gesture arena and just bounced, and
  `TerminalScrollGestureHandler` never got to report the drag as mouse wheel
  events. Claude Code 2.1.x runs in the alternate screen with SGR mouse
  tracking and scrolls its own transcript on wheel events, so without this
  the terminal could not be scrolled at all on the phone.
- `TerminalScrollGestureHandler` records global pointer positions but
  `getCellOffset` expected local ones; the view now converts them.
- Wheel button ids were 68..71 (64 + 4..7); bit 2 is the shift modifier in
  the mouse protocol, so every wheel report read as shift+wheel. They are now
  64..67 as in xterm.

Third change (everything on screen underlined, faint and bold):

- `CSI > 4 ; 2 m` (XTMODKEYS, how Claude Code turns on modifyOtherKeys) was
  handled as SGR: the parser records the `>` prefix and then ignored it, so
  the sequence read as SGR 4 (underline) plus SGR 2 (faint), and everything
  drawn afterwards was underlined and dim. `_csiHandleSgr` now ignores CSI
  sequences with a private prefix.
- SGR 22 unset only faint, but it means "normal intensity" and Claude Code
  closes every bold run with it (`ESC[1m … ESC[22m`), so bold latched on for
  the rest of the session. It now unsets bold as well.
