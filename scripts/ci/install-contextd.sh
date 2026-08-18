#!/usr/bin/env sh
# Install the contextd this build declares it needs.
#
# THE VERSION IS NOT WRITTEN HERE. It is read out of internal/update/update.go,
# where MinContextd already states the oldest Contextverse this build works
# with — the same constant the server compares against at runtime and warns the
# operator about. A second copy of that number in a script is a second thing to
# remember, and the one that goes stale is always the one nobody is looking at.
# That is exactly how the container image came to ship v0.30.0 against a floor
# of 1.0.0.
#
# Used by CI so the contextstore tests run against a real contextd instead of
# skipping, which is what they did — silently, while reporting ok — for as long
# as nothing installed one.
#
# Usage: scripts/ci/install-contextd.sh [destination-directory]
set -eu

DEST="${1:-/usr/local/bin}"
REPO_ROOT="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"

VERSION="$(sed -n 's/^const MinContextd = "\(.*\)"$/\1/p' "$REPO_ROOT/internal/update/update.go")"
if [ -z "$VERSION" ]; then
    echo "could not read MinContextd from internal/update/update.go — has the constant moved?" >&2
    exit 1
fi

case "$(uname -s)" in
    Linux)  OS=linux ;;
    Darwin) OS=darwin ;;
    # Git Bash and MSYS on a Windows runner, where uname reports the emulation
    # layer rather than the system. The asset is still the Windows one.
    MINGW*|MSYS*|CYGWIN*) OS=windows ;;
    *)      echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *)             echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

ASSET="contextd_${VERSION}_${OS}_${ARCH}.tar.gz"
EXE=""
if [ "$OS" = windows ]; then
    ASSET="contextd_${VERSION}_${OS}_${ARCH}.zip"
    EXE=".exe"
fi
BASE="https://github.com/orkcom-tech/contextverse/releases/download/v${VERSION}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "fetching contextd ${VERSION} (${OS}/${ARCH}), the floor this build declares"
curl -fsSL -o "$WORK/$ASSET" "$BASE/$ASSET"
# The checksum file is published beside the archives and is what the release
# signs. Downloading a binary and running it without checking it is the part of
# a supply chain that nobody notices until it matters.
curl -fsSL -o "$WORK/checksums.txt" "$BASE/checksums.txt"

WANT="$(grep " ${ASSET}\$" "$WORK/checksums.txt" | cut -d' ' -f1)"
if [ -z "$WANT" ]; then
    echo "no checksum published for $ASSET" >&2
    exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
    GOT="$(sha256sum "$WORK/$ASSET" | cut -d' ' -f1)"
else
    GOT="$(shasum -a 256 "$WORK/$ASSET" | cut -d' ' -f1)"
fi
if [ "$WANT" != "$GOT" ]; then
    echo "checksum mismatch for $ASSET: published $WANT, downloaded $GOT" >&2
    exit 1
fi

if [ "$OS" = windows ]; then
    unzip -q -o -d "$WORK" "$WORK/$ASSET" "contextd${EXE}"
else
    tar -xzf "$WORK/$ASSET" -C "$WORK" contextd
fi
mkdir -p "$DEST"
install -m 0755 "$WORK/contextd${EXE}" "$DEST/contextd${EXE}"
echo "installed $("$DEST/contextd${EXE}" version) to $DEST/contextd${EXE}"
