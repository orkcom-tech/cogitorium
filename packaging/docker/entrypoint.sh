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

    # `contextd status` answers the question and exits 0 EITHER WAY — a missing
    # space is a fact it reports, not an error. An earlier version tested its
    # exit code, so the condition was never true, init never ran, and every
    # container came up with context unavailable. The answer is in the payload:
    # "exists": false.
    if ! contextd status --json 2>/dev/null | grep -qE '"exists"[[:space:]]*:[[:space:]]*true'; then
        echo "cogitorium: initialising the Contextverse space (first start, HOME=$HOME)"
        # stderr is deliberately NOT discarded. An earlier version sent it to
        # /dev/null and the result was a container where context was unavailable
        # for a reason nobody could read — the failure has to be in the log the
        # operator already looks at. `init solo` fetches its template from
        # GitHub, so the likeliest failure is no outbound network, and that is
        # exactly the message worth keeping.
        if contextd init solo --name cogitorium --role workbench >/dev/null; then
            echo "cogitorium: context space ready"
        else
            echo "cogitorium: contextd init did not succeed; context will report unavailable" >&2
        fi
    fi
fi

# Seed the plugins a derived image baked in, on EVERY start rather than the
# first. That is what makes the plugin set a property of the image: a
# first-start-only seed comes up empty the moment somebody recreates the
# container against a volume that already exists, or the pod lands on a node
# whose volume was provisioned earlier.
#
# Only plugins are copied. Runtimes are read where they already are — a musl
# CPython is well over a hundred megabytes across tens of thousands of files,
# and copying that into the volume on every start would double the storage and
# pay a per-start walk for nothing.
#
# Failure here is reported and not fatal. A server that refused to start
# because an extra could not be copied would take the whole product away over
# something nobody asked for yet.
if [ -d /usr/share/cogitorium/ref/plugins ]; then
    cogitorium plugins seed || echo "cogitorium: the baked plugins could not be seeded" >&2
fi

exec cogitorium "$@"
