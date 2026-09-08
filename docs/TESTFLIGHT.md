# TestFlight from a Linux laptop

`.github/workflows/ios.yml` has a `testflight` job that builds a signed App Store
archive and uploads it. It stays skipped until all seven secrets below exist, so
nothing changes for anyone who does not want to run it.

**You do not need a Mac.** The job runs on a GitHub-hosted macOS runner, and the
one step that normally forces a Mac — creating the signing certificate in
Keychain Access — is done here with `openssl`. Everything else is the Apple
Developer website.

What you do need is the **Apple Developer Program**, $99/year, at
<https://developer.apple.com/programs/>. Enrolment as an individual usually
clears in a day or two; it can ask for ID.

Cost note: each tag now spends macOS runner minutes twice, once for the unsigned
sideload IPA and once for this. On a private repo macOS minutes bill at 10x.

## One-time setup

### 1. Team ID

<https://developer.apple.com/account> → Membership details. Ten characters,
something like `A1B2C3D4E5`.

### 2. Distribution certificate, made on Linux

Apple only needs a certificate signing request; nothing cares that it came from
`openssl` instead of Keychain Access.

```
openssl req -new -newkey rsa:2048 -nodes \
  -keyout dist.key -out dist.csr \
  -subj "/emailAddress=markus@freedomfactory.io/CN=Markus Haas/C=DE"
```

Certificates → **+** → **Apple Distribution** → upload `dist.csr` → download
`distribution.cer`. Then bundle key and certificate into the `.p12` the workflow
imports:

```
openssl x509 -in distribution.cer -inform DER -out dist.pem -outform PEM
openssl pkcs12 -export -inkey dist.key -in dist.pem -out dist.p12
```

It will ask for an export password. Any password, but keep it — it becomes
`APPLE_DIST_CERT_PASSWORD`.

Keep `dist.key` and `dist.p12` somewhere safe and out of the repo. Losing them
is not fatal (revoke the certificate and repeat this step), but the certificate
is valid for three years and you get few of them.

### 3. App ID and provisioning profile

Identifiers → **+** → App IDs → App → explicit bundle ID
`io.freedomfactory.orchestrator`, matching `PRODUCT_BUNDLE_IDENTIFIER` in
`app/ios/Runner.xcodeproj/project.pbxproj`. No capabilities need to be enabled:
the app uses local notifications, not push.

Profiles → **+** → **App Store Connect** → that App ID → that certificate →
download the `.mobileprovision`.

Redo this step whenever the certificate is replaced or a capability is added;
the profile itself expires after a year.

### 4. App Store Connect API key

<https://appstoreconnect.apple.com> → Users and Access → Integrations → App
Store Connect API → **+**, role **App Manager**. Note the Issuer ID and Key ID,
and download the `.p8` — Apple serves it exactly once.

This is what the upload authenticates with, so it needs no password and no
2FA dance in CI.

### 5. App record

App Store Connect → Apps → **+** → New App. Platform iOS, the bundle ID from
step 3, SKU anything (`orchestrator` is fine). The app never has to be submitted
to the App Store for TestFlight to work.

### 6. Secrets

Base64 with `-w0`, or GitHub stores the newlines too:

```
gh secret set APPLE_TEAM_ID                       # A1B2C3D4E5
gh secret set APPLE_DIST_CERT_PASSWORD            # the p12 export password
gh secret set APPSTORE_KEY_ID                     # e.g. 2X9ABC3DEF
gh secret set APPSTORE_ISSUER_ID                  # the uuid on the API keys page

base64 -w0 dist.p12                 | gh secret set APPLE_DIST_CERT_P12
base64 -w0 profile.mobileprovision  | gh secret set APPLE_PROVISIONING_PROFILE
base64 -w0 AuthKey_2X9ABC3DEF.p8    | gh secret set APPSTORE_PRIVATE_KEY
```

The `configured` job checks all seven and, if any is missing, prints which and
skips the macOS job.

## Releasing

Same as before:

```
git tag app-v0.1.5 && git push origin app-v0.1.5
```

The tag builds the unsigned sideload IPA and, in parallel, the signed one. Ten
minutes later the build appears in App Store Connect → TestFlight, briefly as
"Processing".

The build number is the workflow run number, not pubspec's `+N`: App Store
Connect refuses a build number it has already seen, and the run number climbs on
its own. `CFBundleShortVersionString` still comes from the tag.

**Internal testers** (up to 100, all on your own account) install immediately —
no review. **External testers** need one Beta App Review per version, usually a
day, and a public link if you want one.

Export compliance is already answered: `ITSAppUsesNonExemptEncryption` is
`false` in `app/ios/Runner/Info.plist`, on the grounds that the app's crypto is
TLS plus Ed25519 authentication, both exempt. If that ever stops being true —
the app starts encrypting user content itself, say — that key has to change.

## When it breaks

- **"No signing certificate found"** — the p12 did not import. Check the
  password secret, and that `base64 -w0` was used.
- **The profile check fails early** — deliberate. The message prints the App ID
  in the profile against the one expected; usually the profile was made for the
  wrong bundle ID, or `APPLE_TEAM_ID` has a typo.
- **`ITMS-90XXX` on upload** — the archive built but Apple rejected the bundle.
  The `--validate-app` step runs first for exactly this, so the error appears
  before the slow upload.
- **A mail about a missing privacy manifest** — a warning, not a rejection. It
  means some dependency has no `PrivacyInfo.xcprivacy`. Apple only enforces it
  for App Store submission, not TestFlight.
