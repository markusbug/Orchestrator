# Android APK from GitHub Actions

`.github/workflows/android.yml` builds `Orchestrator-<version>.apk` on every
`app-v*` tag and attaches it to the release next to the IPA. It is a universal
APK (arm64, armv7, x86_64) for sideloading; Play still gets a bundle built and
uploaded by hand (`flutter build appbundle --release`, then `scripts/play.py
upload`).

The build itself needs nothing. Signing does: without the four secrets below the
job still builds and keeps the APK as a workflow artifact, but it is debug-signed
and is deliberately **not** attached to the release.

## Secrets

`android/key.properties` is not in the repo, so the runner gets the same four
values as secrets and writes the file itself:

| Secret | Value |
| --- | --- |
| `ANDROID_KEYSTORE` | `base64 -w0 ~/.keystores/orchestrator-release.jks` |
| `ANDROID_KEYSTORE_PASSWORD` | `storePassword` from `android/key.properties` |
| `ANDROID_KEY_ALIAS` | `keyAlias`, currently `orchestrator` |
| `ANDROID_KEY_PASSWORD` | `keyPassword` |
| `ANDROID_CERT_SHA256` | optional: the certificate's SHA-256, lowercase hex, no colons |

With `ANDROID_CERT_SHA256` set the job refuses an APK whose certificate does not
match, which is the only way to notice a silent fallback to the debug key.
Get it from the keystore:

```
keytool -list -keystore ~/.keystores/orchestrator-release.jks -alias orchestrator \
  | sed -n 's/.*(SHA-256): //p' | tr -d : | tr A-F a-f
```

From a checkout with `gh` logged in, all five at once:

```
cd app/android
gh secret set ANDROID_KEYSTORE < <(base64 -w0 ~/.keystores/orchestrator-release.jks)
gh secret set ANDROID_KEYSTORE_PASSWORD --body "$(sed -n 's/^storePassword=//p' key.properties)"
gh secret set ANDROID_KEY_ALIAS --body "$(sed -n 's/^keyAlias=//p' key.properties)"
gh secret set ANDROID_KEY_PASSWORD --body "$(sed -n 's/^keyPassword=//p' key.properties)"
gh secret set ANDROID_CERT_SHA256 --body "$(keytool -list -keystore ~/.keystores/orchestrator-release.jks \
  -alias orchestrator -storepass "$(sed -n 's/^storePassword=//p' key.properties)" \
  | sed -n 's/.*(SHA-256): //p' | tr -d : | tr A-F a-f)"
```

## Which key is which

Play App Signing is enrolled, so Google re-signs the bundle with its own app
signing key and `orchestrator-release.jks` is the *upload* key. The sideload
APK is signed with that upload key directly. Android treats the two as
different apps' signatures: moving a phone from the Play build to the APK, or
back, means uninstalling first. Same package name, same data path, so the
pairing has to be redone.

## By hand

The same thing locally, signed from `android/key.properties`:

```
cd app && flutter build apk --release
cp build/app/outputs/flutter-apk/app-release.apk ../dist/Orchestrator-0.1.7.apk
gh release upload app-v0.1.7 ../dist/Orchestrator-0.1.7.apk
```

Build from the tagged commit, not from `main`, so the APK matches the IPA that
is already on the release.
