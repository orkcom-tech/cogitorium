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
    # contextd keeps its space under HOME. In the compose image that is the
    # user's own directory, but under Kubernetes the root filesystem is
    # read-only and HOME is moved onto the data volume — where, on a freshly
    # provisioned PVC, it does not exist yet. Without this the init below fails
    # with "no such file or directory" and the install comes up with memory
    # quietly doing nothing.
    if [ -n "$HOME" ] && [ ! -d "$HOME" ]; then
        mkdir -p "$HOME" || echo "cogitorium: could not create HOME=$HOME; context will report unavailable" >&2
    fi

    if ! contextd status --json >/dev/null 2>&1; then
        echo "cogitorium: initialising the Contextverse space (first start, HOME=$HOME)"
        # stderr is deliberately NOT discarded. An earlier version sent it to
        # /dev/null and the result was a cluster where context was unavailable
        # for a reason nobody could read — the failure has to be in the log the
        # operator already looks at.
        if contextd init solo --name cogitorium --role workbench >/dev/null; then
            echo "cogitorium: context space ready"
        else
            echo "cogitorium: contextd init did not succeed; context will report unavailable" >&2
        fi
    fi
fi

exec cogitorium "$@"
