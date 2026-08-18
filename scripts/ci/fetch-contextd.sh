#!/usr/bin/env sh
# Fetch contextd for every target the release builds, so the artifacts carry it.
#
# WHY THE ARTIFACTS CARRY IT. Installing Cogitorium has to produce a working
# product. Homebrew, Scoop and winget can say "and this other package too" and
# their package managers act on it; a downloaded tarball and a .deb cannot say
# anything at all, so for those two the only way to satisfy the dependency is to
# be carrying it. Anything else is an errand handed to the person who just
# installed the thing.
#
# The version is read from MinContextd, like scripts/ci/install-contextd.sh, so
# there is still exactly one place that states which Contextverse this build
# goes with.
#
# NOT INTO dist/. goreleaser cleans dist and then checks it is empty before
# building; a before-hook that writes there aborts the run with "dist is not
# empty" even under --clean. The staging directory is a sibling.
set -eu

REPO_ROOT="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
STAGE="${1:-$REPO_ROOT/build/contextd}"

VERSION="$(sed -n 's/^const MinContextd = "\(.*\)"$/\1/p' "$REPO_ROOT/internal/update/update.go")"
if [ -z "$VERSION" ]; then
    echo "could not read MinContextd from internal/update/update.go" >&2
    exit 1
fi

BASE="https://github.com/orkcom-tech/contextverse/releases/download/v${VERSION}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

curl -fsSL -o "$WORK/checksums.txt" "$BASE/checksums.txt"

rm -rf "$STAGE"
mkdir -p "$STAGE"

# The same six the release builds. Named rather than derived, so a target added
# to .goreleaser.yaml without a contextd to match it fails the archive step with
# a missing file rather than shipping an incomplete artifact quietly.
for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64; do
    asset="contextd_${VERSION}_${target}.tar.gz"
    case "$target" in windows_*) asset="contextd_${VERSION}_${target}.zip" ;; esac

    echo "  contextd ${VERSION} ${target}"
    curl -fsSL -o "$WORK/$asset" "$BASE/$asset"

    want="$(grep " ${asset}\$" "$WORK/checksums.txt" | cut -d' ' -f1)"
    if [ -z "$want" ]; then
        echo "no checksum published for $asset" >&2
        exit 1
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        got="$(sha256sum "$WORK/$asset" | cut -d' ' -f1)"
    else
        got="$(shasum -a 256 "$WORK/$asset" | cut -d' ' -f1)"
    fi
    if [ "$want" != "$got" ]; then
        echo "checksum mismatch for $asset: published $want, downloaded $got" >&2
        exit 1
    fi

    # One directory per target, because goreleaser templates `src` but NOT
    # `dst` — a dst of "contextd{{ if eq .Os \"windows\" }}.exe{{ end }}" ships
    # a file whose NAME is that template. So the layout carries the platform
    # and strip_parent puts the binary at the root of the archive.
    mkdir -p "$STAGE/$target"
    case "$target" in
        windows_*) unzip -q -o -d "$STAGE/$target" "$WORK/$asset" contextd.exe ;;
        *)         tar -xzf "$WORK/$asset" -C "$STAGE/$target" contextd ;;
    esac
    chmod 0755 "$STAGE/$target"/contextd* 2>/dev/null || true

    # Contextverse's own licence, taken from the archive it came in and shipped
    # beside it. Cogitorium is Apache-2.0 and Contextverse is BUSL-1.1; they are
    # both ORKCOM's, so bundling is the owner's to decide, but a binary
    # redistributed without its licence text is a defect whoever owns it.
    if [ ! -f "$STAGE/LICENSE-contextverse" ]; then
        case "$target" in
            windows_*) unzip -q -o -d "$WORK/lic" "$WORK/$asset" LICENSE 2>/dev/null || true ;;
            *)         (cd "$WORK" && mkdir -p lic && tar -xzf "$asset" -C lic LICENSE 2>/dev/null) || true ;;
        esac
        if [ -f "$WORK/lic/LICENSE" ]; then
            cp "$WORK/lic/LICENSE" "$STAGE/LICENSE-contextverse"
        else
            echo "contextd's archive carries no LICENSE; refusing to redistribute it without one" >&2
            exit 1
        fi
    fi
done

echo "contextd ${VERSION} staged for six targets in $STAGE"
