#!/bin/sh
# Install Orchestrator on Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/markusbug/Orchestrator/main/scripts/install.sh | sh
#
# Picks the .deb when dpkg is available and the AppImage otherwise. On arm64,
# or with --headless, installs just the daemon and pairs from the terminal,
# because the desktop app is x86_64-only for now.
#
# Flags: --version <tag>  install a specific desktop-v* release
#        --headless       daemon only, no desktop app
#        --help
set -eu

REPO=markusbug/Orchestrator
VERSION=
HEADLESS=0

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# Not read out of $0: piped through `curl | sh` there is no script file.
usage() {
	cat <<'EOF'
Install Orchestrator on Linux.

  --version <tag>   install a specific desktop-v* release
  --headless        install only the daemon, no desktop app
  --help            this text
EOF
}

while [ $# -gt 0 ]; do
	case $1 in
	--version) VERSION=$2; shift 2 ;;
	--headless) HEADLESS=1; shift ;;
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
	VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases" |
		sed -n 's/.*"tag_name": *"\(desktop-v[^"]*\)".*/\1/p' | head -1)
	[ -n "$VERSION" ] || die "could not find a desktop-v* release; pass --version"
fi
BASE="https://github.com/$REPO/releases/download/$VERSION"
ver=${VERSION#desktop-v}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Every artifact is checked against the release's SHA256SUMS.
verify() {
	[ -f "$tmp/SHA256SUMS" ] || curl -fsSL -o "$tmp/SHA256SUMS" "$BASE/SHA256SUMS" || {
		echo "  note: no SHA256SUMS in this release, skipping verification" >&2
		return 0
	}
	command -v sha256sum >/dev/null || return 0
	(cd "$tmp" && grep " $1\$" SHA256SUMS | sha256sum -c -) ||
		die "checksum mismatch for $1"
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
