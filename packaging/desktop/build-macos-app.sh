#!/usr/bin/env bash
# Assemble Cogitorium.app around an already-built binary.
#
# The bundle is a directory with a rulebook, not a format that needs a tool:
# a plist, an executable in Contents/MacOS, an icon in Contents/Resources. So
# this is shell rather than a dependency, and it can be read in one sitting.
#
# Unsigned. There is no Apple Developer identity for this project, so Gatekeeper
# will refuse the first launch and the documentation says exactly how to get
# past it. Pretending otherwise — shipping a "signed" app that is not — would be
# worse than the extra paragraph.
set -euo pipefail

BIN="${1:?usage: build-macos-app.sh <binary> <version> [outdir]}"
VERSION="${2:?}"
OUT="${3:-dist}"
APP="$OUT/Cogitorium.app"
HERE="$(cd "$(dirname "$0")" && pwd)"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
sed "s|__VERSION__|${VERSION#v}|g" "$HERE/Info.plist" > "$APP/Contents/Info.plist"
cp "$BIN" "$APP/Contents/MacOS/cogitorium-desktop"
chmod +x "$APP/Contents/MacOS/cogitorium-desktop"
cp "$HERE/Cogitorium.icns" "$APP/Contents/Resources/Cogitorium.icns"

echo "built $APP"
