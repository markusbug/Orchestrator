# Orchestrator app

Flutter client for the Orchestrator daemon. iOS first (sideloaded), Android later from the same code.

```
flutter pub get
flutter analyze
flutter test                       # unit tests + a fake daemon over TLS
flutter test test/live_test.dart   # against a real daemon; see the file header for the env vars
```

Layout:

- `lib/protocol/` binary frames and the auth challenge
- `lib/model/` hosts, sessions, filesystem entries
- `lib/services/` Keychain keys, host persistence, settings, notifications, QR payload parsing
- `lib/net/` `HostClient` (one socket), `HostConnection` (per-host reconnect, sessions, terminal bindings), `AppModel` (all hosts, lifecycle, connectivity)
- `lib/ui/` hosts, pairing, sessions, new session, command sheet, terminal, settings

The iOS build happens in GitHub Actions (`.github/workflows/ios.yml`) on `app-v*` tags and produces an unsigned IPA. Signing and installing from Ubuntu is described in `../MVP.md`.
