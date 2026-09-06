#!/usr/bin/env bash
# Assemble the Linux desktop artifacts: a .deb and an AppImage.
#
# Usage: packaging/build-linux.sh [--version X.Y.Z] [--arch amd64] [--skip-flutter]
#
# Expects the Flutter Linux bundle to have been built already (or builds it),
# and the daemon binary at bin/orchestrator for the target architecture.
# Output lands in packaging/out and dist/.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$PWD

VERSION=""
ARCH=amd64
SKIP_FLUTTER=0
FLUTTER=${FLUTTER:-flutter}

while [ $# -gt 0 ]; do
	case $1 in
	--version) VERSION=$2; shift 2 ;;
	--arch) ARCH=$2; shift 2 ;;
	--skip-flutter) SKIP_FLUTTER=1; shift ;;
	-h|--help) sed -n '2,8p' "$0"; exit 0 ;;
	*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

if [ -z "$VERSION" ]; then
	VERSION=$(git describe --tags --match 'desktop-v*' --abbrev=0 2>/dev/null | sed 's/^desktop-v//' || true)
	[ -n "$VERSION" ] || VERSION=0.0.0
fi

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

OUT=$ROOT/packaging/out
BUNDLE_SRC=$ROOT/app/build/linux/x64/release/bundle
LIBDIR=$OUT/root/usr/lib/orchestrator

rm -rf "$OUT"
mkdir -p "$LIBDIR" "$ROOT/dist"

if [ "$SKIP_FLUTTER" -eq 0 ]; then
	log "building the Flutter bundle"
	(cd app && "$FLUTTER" build linux --release -t lib/main_desktop.dart)
fi
[ -d "$BUNDLE_SRC" ] || { echo "missing $BUNDLE_SRC" >&2; exit 1; }

log "assembling /usr/lib/orchestrator"
cp -r "$BUNDLE_SRC"/. "$LIBDIR/"
[ -f "$ROOT/bin/orchestrator" ] || { echo "missing bin/orchestrator (run make build)" >&2; exit 1; }
install -m 0755 "$ROOT/bin/orchestrator" "$LIBDIR/orchestrator"

log "building the .deb"
if ! command -v nfpm >/dev/null; then
	echo "nfpm not found: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest" >&2
	exit 1
fi
PKG_ARCH=$ARCH PKG_VERSION=$VERSION nfpm package \
	--config packaging/nfpm.yaml --packager deb --target "$ROOT/dist/"

log "building the AppImage"
APPDIR=$OUT/Orchestrator.AppDir
mkdir -p "$APPDIR/usr/lib"
cp -r "$OUT/root/usr/lib/orchestrator" "$APPDIR/usr/lib/orchestrator"
install -Dm755 packaging/appimage/AppRun "$APPDIR/AppRun"
install -Dm644 packaging/linux/io.freedomfactory.orchestrator.desktop \
	"$APPDIR/io.freedomfactory.orchestrator.desktop"
install -Dm644 packaging/linux/orchestrator-256.png "$APPDIR/orchestrator.png"

# Libraries a stock desktop may not have, and which Flutter does not ship.
# Missing ones are skipped rather than fatal: the AppImage still runs, it just
# falls back to the host copy.
for so in libayatana-appindicator3.so.1 libayatana-indicator3.so.7 \
	libayatana-ido3-0.4.so.0 libdbusmenu-glib.so.4 libdbusmenu-gtk3.so.4 \
	libsecret-1.so.0; do
	# No `exit` in the awk: quitting early closes the pipe under ldconfig,
	# and pipefail turns that SIGPIPE into a fatal error.
	path=$(ldconfig -p | awk -v s="$so" '$1 == s && !seen { print $NF; seen = 1 }')
	if [ -n "$path" ]; then
		cp -L "$path" "$APPDIR/usr/lib/orchestrator/lib/"
	else
		echo "  note: $so not found on this machine, not bundled" >&2
	fi
done

if ! command -v appimagetool >/dev/null; then
	echo "appimagetool not found: download it from" >&2
	echo "  https://github.com/AppImage/AppImageKit/releases/download/continuous/appimagetool-x86_64.AppImage" >&2
	exit 1
fi
ARCH_APPIMAGE=x86_64
if [ "$ARCH" = arm64 ]; then
	ARCH_APPIMAGE=aarch64
fi
ARCH=$ARCH_APPIMAGE appimagetool --appimage-extract-and-run \
	"$APPDIR" "$ROOT/dist/Orchestrator-$VERSION-$ARCH_APPIMAGE.AppImage"

log "done"
ls -la "$ROOT/dist"
