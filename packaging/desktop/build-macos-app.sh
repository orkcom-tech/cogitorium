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

# Contextverse, INSIDE the bundle, beside the executable.
#
# Somebody who drags a .app into Applications has installed one thing and has
# nowhere to put a second binary. Contents/MacOS is where the server looks —
# findContextd checks the directory of its own executable when PATH has no
# contextd — so this is what makes double-clicking the app produce a working
# product rather than one whose Context screen is dead.
#
# The caller places it; this refuses to assemble a bundle without it rather
# than shipping one silently missing half of itself.
CONTEXTD="$(dirname "$BIN")/contextd"
if [ ! -x "$CONTEXTD" ]; then
    echo "no contextd at $CONTEXTD — run scripts/ci/install-contextd.sh into that directory first" >&2
    exit 1
fi
cp "$CONTEXTD" "$APP/Contents/MacOS/contextd"
chmod +x "$APP/Contents/MacOS/contextd"
cp "$HERE/Cogitorium.icns" "$APP/Contents/Resources/Cogitorium.icns"

# The licence and its NOTICE ride inside the bundle.
#
# The zip that ships to users contains Cogitorium.app and nothing else, so
# before this the macOS download was the only artifact carrying neither — a
# redistribution with the terms left behind. Resources/ is where a .app is
# expected to keep them.
ROOT="$(cd "$HERE/../.." && pwd)"
cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$APP/Contents/Resources/"

echo "built $APP"
