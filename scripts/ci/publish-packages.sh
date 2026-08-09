#!/usr/bin/env bash
# Point the Homebrew tap and the Scoop bucket at a Cogitorium release.
#
# Both live in their own repositories with their own history, which is why they
# are bumped after the release rather than generated during it: a release that
# half-succeeds must never leave a package manager pointing at artifacts that
# do not exist.
#
# Usage:
#   PACKAGING_TOKEN=ghp_… ./scripts/ci/publish-packages.sh v0.1.0
set -euo pipefail

TAG="${1:-}"
[[ -n "$TAG" ]] || { echo "usage: $0 <tag>  (e.g. v0.1.0)" >&2; exit 2; }
[[ -n "${PACKAGING_TOKEN:-}" ]] || { echo "PACKAGING_TOKEN is required" >&2; exit 1; }

VERSION="${TAG#v}"
REPO="${COGITORIUM_REPO:-orkcom-tech/cogitorium}"
HOMEBREW_TAP_REPO="${HOMEBREW_TAP_REPO:-orkcom-tech/homebrew-tap}"
SCOOP_BUCKET_REPO="${SCOOP_BUCKET_REPO:-orkcom-tech/scoop-bucket}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The checksums file is the release's own record of what it produced. Deriving
# the hashes from it rather than re-downloading each archive means the manifest
# can only ever describe artifacts this release actually published.
echo "==> Fetching checksums for $TAG"
curl -fsSL "https://github.com/${REPO}/releases/download/${TAG}/checksums.txt" -o "$TMP/checksums.txt"

hash_for() {
  local file="$1"
  awk -v f="$file" '$2 == f || $2 == "*"f { print $1 }' "$TMP/checksums.txt" | head -1
}

need() {
  local v="$1" what="$2"
  [[ -n "$v" ]] || { echo "no checksum for $what in the release — refusing to write a manifest that lies" >&2; exit 1; }
}

DARWIN_ARM64="$(hash_for "cogitorium_${VERSION}_darwin_arm64.tar.gz")"; need "$DARWIN_ARM64" darwin/arm64
DARWIN_AMD64="$(hash_for "cogitorium_${VERSION}_darwin_amd64.tar.gz")"; need "$DARWIN_AMD64" darwin/amd64
LINUX_ARM64="$(hash_for "cogitorium_${VERSION}_linux_arm64.tar.gz")";   need "$LINUX_ARM64" linux/arm64
LINUX_AMD64="$(hash_for "cogitorium_${VERSION}_linux_amd64.tar.gz")";   need "$LINUX_AMD64" linux/amd64
WIN_ARM64="$(hash_for "cogitorium_${VERSION}_windows_arm64.zip")";      need "$WIN_ARM64" windows/arm64
WIN_AMD64="$(hash_for "cogitorium_${VERSION}_windows_amd64.zip")";      need "$WIN_AMD64" windows/amd64

export GH_TOKEN="$PACKAGING_TOKEN"
export GITHUB_TOKEN="$PACKAGING_TOKEN"

push_repo() {
  local repo="$1" dir="$2" message="$3"
  (
    cd "$dir"
    git config user.name "github-actions[bot]"
    git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
    if git diff --quiet; then
      echo "==> No change in $repo (already at $TAG)"
      return 0
    fi
    git add -A
    git commit -m "$message"
    git push origin HEAD
    echo "==> Pushed $repo"
  )
}

echo "==> Homebrew: $HOMEBREW_TAP_REPO"
git clone --depth 1 "https://x-access-token:${PACKAGING_TOKEN}@github.com/${HOMEBREW_TAP_REPO}.git" "$TMP/tap"
mkdir -p "$TMP/tap/Formula"
sed \
  -e "s|__VERSION__|${VERSION}|g" \
  -e "s|__DARWIN_ARM64__|${DARWIN_ARM64}|" \
  -e "s|__DARWIN_AMD64__|${DARWIN_AMD64}|" \
  -e "s|__LINUX_ARM64__|${LINUX_ARM64}|" \
  -e "s|__LINUX_AMD64__|${LINUX_AMD64}|" \
  packaging/homebrew/cogitorium.rb.tmpl > "$TMP/tap/Formula/cogitorium.rb"
push_repo "$HOMEBREW_TAP_REPO" "$TMP/tap" "chore: bump cogitorium to ${TAG}"

echo "==> Scoop: $SCOOP_BUCKET_REPO"
git clone --depth 1 "https://x-access-token:${PACKAGING_TOKEN}@github.com/${SCOOP_BUCKET_REPO}.git" "$TMP/bucket"
sed \
  -e "s|__VERSION__|${VERSION}|g" \
  -e "s|__WIN_AMD64__|${WIN_AMD64}|" \
  -e "s|__WIN_ARM64__|${WIN_ARM64}|" \
  packaging/scoop/cogitorium.json.tmpl > "$TMP/bucket/cogitorium.json"
push_repo "$SCOOP_BUCKET_REPO" "$TMP/bucket" "chore: bump cogitorium to ${TAG}"

echo "==> Done. Winget is a manual PR — see packaging/README.md."
