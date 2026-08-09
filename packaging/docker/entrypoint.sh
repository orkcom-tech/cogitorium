#!/bin/sh
# Make the container's Contextverse space usable on first start.
#
# Requirement 15 is that Cogitorium installs together with Contextverse. The
# image carries contextd, but a binary is not a working space: without
# `init solo` the server starts, reports context as unavailable, and memory
# does nothing — which is a fresh `docker compose up` that looks fine and is
# half broken.
#
# Idempotent, and never fatal. If contextd is missing or init fails, the server
# still starts and says so at /api/v1/context/status — the same thing it does
# on a machine where Contextverse was never installed. Refusing to boot over
# this would be worse: everything except memory works.
set -e

if command -v contextd >/dev/null 2>&1; then
    if ! contextd status --json >/dev/null 2>&1; then
        echo "cogitorium: initialising the Contextverse space (first start)"
        contextd init solo --name cogitorium --role workbench >/dev/null 2>&1 \
            || echo "cogitorium: contextd init did not succeed; context will report unavailable" >&2
    fi
fi

exec cogitorium "$@"
