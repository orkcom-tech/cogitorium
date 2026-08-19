#!/bin/sh
# Rebuild the embedded JavaScript engine.
#
# Needs nothing but the Go toolchain this project already requires — no
# wasi-sdk, no emscripten, no node. That is the whole reason the engine is
# written in Go rather than vendored as a QuickJS build: anybody can reproduce
# this file and check it against what is committed.
#
#   sh internal/jsrt/generate.sh
#
# A test fails when the committed artifact no longer matches the source it was
# built from, so this is not a step somebody has to remember.
set -eu
cd "$(dirname "$0")/guest"

GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o /tmp/cog-js-engine.wasm .
gzip -9 -c /tmp/cog-js-engine.wasm > ../engine.wasm.gz
rm -f /tmp/cog-js-engine.wasm

cd ..
# The stamp is what the staleness test compares. Content, not timestamps: a
# checkout has whatever mtimes git felt like, and a rebuild that produced an
# identical engine should not read as a change.
find guest -name '*.go' -o -name 'go.mod' -o -name 'go.sum' | sort | xargs shasum -a 256 \
  | shasum -a 256 | cut -d' ' -f1 > engine.source-hash

printf 'engine: %s\n' "$(ls -lh engine.wasm.gz | awk '{print $5}')"
