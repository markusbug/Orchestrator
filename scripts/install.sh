#!/bin/sh
# Install Orchestrator on Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/markusbug/Orchestrator/main/scripts/install.sh | sh
#
# Picks the .deb when dpkg is available and the AppImage otherwise. On arm64,
# or with --headless, installs just the daemon and pairs from the terminal,
# because the desktop app is x86_64-only for now.
#
# Every download is checked against the release's SHA256SUMS, and against its
# Sigstore build provenance when `gh` is installed. Verification fails closed.
#
# Flags: --version <tag>       install a specific desktop-v* release
#        --headless            daemon only, no desktop app
#        --require-provenance  refuse to install unattested artifacts
#        --skip-verify         install without checking; last resort
#        --help
set -eu

REPO=markusbug/Orchestrator
VERSION=
HEADLESS=0
SKIP_VERIFY=0
REQUIRE_PROVENANCE=0

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# Not read out of $0: piped through `curl | sh` there is no script file.
usage() {
	cat <<'EOF'
Install Orchestrator on Linux.

  --version <tag>        install a specific desktop-v* release
  --headless             install only the daemon, no desktop app
  --require-provenance   refuse to install without verified build provenance
                         (needs the GitHub CLI)
  --skip-verify          install without checking anything; last resort
  --help                 this text
EOF
}

while [ $# -gt 0 ]; do
	case $1 in
	--version) VERSION=$2; shift 2 ;;
	--headless) HEADLESS=1; shift ;;
	--require-provenance) REQUIRE_PROVENANCE=1; shift ;;
	--skip-verify) SKIP_VERIFY=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown flag: $1" ;;
	esac
done

command -v curl >/dev/null || die "curl is required"

os=$(uname -s)
if [ "$os" = Darwin ]; then
	cat <<'EOF'
Orchestrator for macOS ships as a .dmg. Download the latest one from

  https://github.com/markusbug/Orchestrator/releases

drag Orchestrator.app to Applications, and because the app is not yet
notarized, clear the download quarantine flag before opening it:

  xattr -dr com.apple.quarantine /Applications/Orchestrator.app
EOF
	exit 0
fi
[ "$os" = Linux ] || die "unsupported system: $os"

case $(uname -m) in
x86_64) arch=amd64; appimage_arch=x86_64 ;;
aarch64|arm64) arch=arm64; appimage_arch=aarch64; HEADLESS=1 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
	log "looking up the latest release"
	VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases" 2>/dev/null |
		sed -n 's/.*"tag_name": *"\(desktop-v[^"]*\)".*/\1/p' | head -1)
	if [ -z "$VERSION" ]; then
		# While the repository is private this is what you get: the API and
		# every asset URL answer 404 to an anonymous request.
		die "no desktop-v* release found.

If the repository is still private, this script cannot reach it. Use the
GitHub CLI instead:

  gh release download desktop-v0.1.0 -R $REPO -p 'orchestrator_*_amd64.deb'
  sudo apt-get install -y ./orchestrator_*_amd64.deb

Otherwise pass --version <tag> explicitly."
	fi
fi
BASE="https://github.com/$REPO/releases/download/$VERSION"
ver=${VERSION#desktop-v}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Every artifact is checked against the release's SHA256SUMS, and against the
# release's build provenance when the GitHub CLI is on hand. The checksum leg
# fails closed: an install that cannot verify is an install that does not
# happen, because silently skipping verification is worth exactly as much to
# an attacker as no verification at all.
verify() {
	name=$1
	if [ "$SKIP_VERIFY" -eq 1 ]; then
		echo "  warning: --skip-verify given, installing $name unchecked" >&2
		return 0
	fi
	command -v sha256sum >/dev/null ||
		die "sha256sum is needed to verify the download (or pass --skip-verify)"
	[ -f "$tmp/SHA256SUMS" ] || curl -fsSL -o "$tmp/SHA256SUMS" "$BASE/SHA256SUMS" ||
		die "$VERSION publishes no SHA256SUMS, so $name cannot be verified"
	# Exact filename match: a grep pattern would treat the dots in a version
	# as wildcards and could pick up a neighbouring artifact's line.
	awk -v n="$name" '$2 == n || $2 == "*" n' "$tmp/SHA256SUMS" >"$tmp/want"
	[ -s "$tmp/want" ] || die "$name is not listed in SHA256SUMS"
	(cd "$tmp" && sha256sum -c - <want >/dev/null) ||
		die "checksum mismatch for $name -- do not install it"
	verify_provenance "$name"
}

# SHA256SUMS sits in the same release as the artifact, so whoever could
# replace one could replace the other: it proves the bytes arrived intact,
# not that the release is ours. Provenance is what proves that -- it is
# signed on the runner through Sigstore and cannot be minted by a token that
# only uploads release assets.
#
# gh looks an attestation up by the artifact's digest, so a tampered file and
# a release built before provenance existed fail identically: "none found".
# That is why the default is a note rather than an error, and why the note is
# worth little on its own -- --require-provenance is the setting that has
# teeth. Once every supported release carries provenance, make it the default.
verify_provenance() {
	command -v gh >/dev/null || {
		if [ "$REQUIRE_PROVENANCE" -eq 1 ]; then
			die "--require-provenance needs the GitHub CLI (gh) installed"
		fi
		return 0
	}
	if out=$(gh attestation verify "$tmp/$1" -R "$REPO" 2>&1); then
		echo "  provenance verified for $1"
		return 0
	fi
	if [ "$REQUIRE_PROVENANCE" -eq 1 ]; then
		die "provenance verification failed for $1 -- do not install it:
$out"
	fi
	echo "  note: could not verify provenance for $1; re-run with" >&2
	echo "        --require-provenance to make this fatal" >&2
}

install_headless() {
	name="orchestrator_linux_$arch"
	log "downloading the daemon ($arch)"
	curl -fsSL -o "$tmp/$name" "$BASE/$name"
	verify "$name"
	mkdir -p "$HOME/.local/bin"
	install -m 0755 "$tmp/$name" "$HOME/.local/bin/orchestrator"
	log "installed $HOME/.local/bin/orchestrator"
	case ":$PATH:" in
	*":$HOME/.local/bin:"*) ;;
	*) echo "  add $HOME/.local/bin to your PATH" ;;
	esac
	"$HOME/.local/bin/orchestrator" install
	echo
	echo "Now run 'orchestrator pair' to get a QR code for your phone."
}

install_deb() {
	name="orchestrator_${ver}_${arch}.deb"
	log "downloading $name"
	curl -fsSL -o "$tmp/$name" "$BASE/$name"
	verify "$name"
	log "installing (sudo)"
	# apt rather than dpkg -i, so the GTK and appindicator deps resolve.
	sudo apt-get install -y "$tmp/$name"
}

install_appimage() {
	name="Orchestrator-${ver}-${appimage_arch}.AppImage"
	log "downloading $name"
	curl -fsSL -o "$tmp/$name" "$BASE/$name"
	verify "$name"
	mkdir -p "$HOME/.local/bin" "$HOME/.local/share/applications"
	install -m 0755 "$tmp/$name" "$HOME/.local/bin/Orchestrator.AppImage"
	cat >"$HOME/.local/share/applications/io.freedomfactory.orchestrator.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Orchestrator
Comment=Run Claude Code sessions on this machine, drive them from your phone
Exec=$HOME/.local/bin/Orchestrator.AppImage
Terminal=false
Categories=Development;Utility;
EOF
	command -v update-desktop-database >/dev/null &&
		update-desktop-database "$HOME/.local/share/applications" || true
	log "installed $HOME/.local/bin/Orchestrator.AppImage"
}

if [ "$HEADLESS" -eq 1 ]; then
	install_headless
	exit 0
fi

if command -v dpkg >/dev/null && command -v sudo >/dev/null; then
	install_deb
else
	install_appimage
fi

cat <<'EOF'

Done. Open Orchestrator from your applications menu, or run:

  orchestrator

The first launch offers to start the background service and shows a QR
code to pair your phone.

If the tray icon does not appear on GNOME, enable the AppIndicator
extension:

  gnome-extensions enable ubuntu-appindicators@ubuntu.com
EOF
