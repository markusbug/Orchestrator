# TestFlight — external testing setup

Internal testers already work with no review. Everything below is what App Store
Connect asks for before a build can go to **external** testers, plus the optional
public link.

App Store Connect → Orc Terminal → TestFlight.

## 1. Test Information (asked once, reused per build)

**Beta app description**

```
Claude Code runs on your laptop. Orc Terminal drives it from your phone.

Start a session in any folder on your machine and walk away — your laptop keeps it
running, so locking the phone or losing signal changes nothing. Come back later and
the terminal is exactly where you left it. It is a real terminal: scrollback, arrow
keys, Ctrl-C, resize, and as many sessions at once as you like.

You need a Linux or macOS computer running Claude Code with the Orchestrator service
installed on it. Setup is: install on the computer, install on the phone, scan one QR
code.
```

**What to test**

```
1. Pairing. Scan the QR code your computer shows. It should connect in a second or
   two, and the host should appear with a green dot.
2. Walking away. Start a session, lock the phone or switch apps for a while, then come
   back. The session should still be running with its screen intact.
3. Losing the network. Turn Wi-Fi off mid-session and let it fall back to cellular, or
   go through a tunnel. It should reconnect on its own and replay the screen.
4. Being asked a question. When Claude Code stops to ask something, the session should
   jump to the top of the list and the phone should buzz within a couple of seconds,
   even with the app closed.
5. The keyboard. Typing prompts, the key bar (Esc, Ctrl, arrows, Tab), submitting with
   return, and inserting a newline with the New line key.
6. Rotation. Turn the phone while a full-screen TUI is running; the terminal should
   resize and redraw rather than tear.

Please say which iPhone and which iOS version, and what the terminal was running.
```

**Feedback email**: `hm.orc@protonmail.ch`
**Marketing URL**: `https://orc.markushaas.com`
**Privacy policy URL**: `https://orc.markushaas.com/privacy`

## 2. Create the external group

TestFlight → Testers and Groups → **+** → name it something like "Early access".
Add the build to the group. Adding a build to an external group is what triggers
**Beta App Review** — usually about a day, and only once per version, not per build.

## 3. Public link (optional)

On the group, enable **Public Link**. That gives a URL anyone can open to install,
with a tester cap you set. Without it, you add testers by email address one at a time.

If you enable it, put the link on the landing page in place of the mailto button —
that is a much lower-friction call to action than asking people to write an email.
Tell me and I will make that change.

## 4. Export compliance

Already answered in the project: `ITSAppUsesNonExemptEncryption` is `false` in
`app/ios/Runner/Info.plist`, on the grounds that the app's crypto is TLS plus Ed25519
authentication, both exempt. Nothing to fill in per build. If the app ever starts
encrypting user content itself, that key has to change.

## Note on builds

The icon changed after 0.1.5, so build 7 in TestFlight still shows the Flutter logo.
Use a build from `app-v0.1.6` or later for anything external.
