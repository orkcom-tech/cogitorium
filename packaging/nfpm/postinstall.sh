#!/bin/sh
# Make the installed package actually startable, then say what is still missing.
#
# The unit this package ships declares User=cogitorium, Group=cogitorium and
# --data /var/lib/cogitorium. None of those exist on a fresh machine, and an
# earlier version of this script created none of them — so `apt install ./…deb`
# succeeded, `systemctl start cogitorium` failed inside systemd, and the error
# the operator saw had nothing to do with Cogitorium. A package that installs
# something it cannot start is worse than one that refuses to install.
#
# Everything here is idempotent: package managers run postinstall on upgrade as
# well as on first install, and an upgrade must not disturb an account or a
# data directory that is already in use.
set -e

DATA_DIR=/var/lib/cogitorium
SVC_USER=cogitorium
SVC_GROUP=cogitorium

# The service account. No login shell and no home of its own: it exists to own
# the data directory and to be the identity the unit drops to, and nothing else.
if ! getent group "$SVC_GROUP" >/dev/null 2>&1; then
    if command -v groupadd >/dev/null 2>&1; then
        groupadd --system "$SVC_GROUP"
    elif command -v addgroup >/dev/null 2>&1; then
        addgroup --system "$SVC_GROUP"
    fi
fi

if ! getent passwd "$SVC_USER" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
        useradd --system --gid "$SVC_GROUP" --home-dir "$DATA_DIR" \
                --no-create-home --shell /usr/sbin/nologin \
                --comment "Cogitorium service account" "$SVC_USER"
    elif command -v adduser >/dev/null 2>&1; then
        adduser --system --ingroup "$SVC_GROUP" --home "$DATA_DIR" \
                --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
    fi
fi

# 0750, not 0755. The database in here holds every provider API key the
# operator configured; there is no reason for the rest of the machine to read
# it. The unit already declares ReadWritePaths=/var/lib/cogitorium, so this is
# the only path the service can write at all.
mkdir -p "$DATA_DIR"
if getent passwd "$SVC_USER" >/dev/null 2>&1; then
    chown "$SVC_USER":"$SVC_GROUP" "$DATA_DIR"
fi
chmod 0750 "$DATA_DIR"

# Contextverse is IN this package, at /usr/libexec/cogitorium/contextd, and the
# systemd unit points the server at it. Nothing to fetch, nothing to tell the
# operator to go and do.
#
# It used to be a `Recommends` plus a paragraph printed here saying where to
# find contextd — which left the person who had just installed a product with
# an errand, and a server whose memory silently did nothing until they ran it.
# The reasoning for not using `Depends` still stands (contextd is not in any
# distribution repository, so a hard dependency would make this package
# refuse to install); carrying the binary is what that reasoning was missing.
#
# The context SPACE is created by the server on first start rather than here:
# it runs as the cogitorium user with HOME on the data directory, and doing it
# from a postinstall running as root would create the space with the wrong
# owner. See EnsureSpace in internal/contextstore.
if [ ! -x /usr/libexec/cogitorium/contextd ]; then
    echo "cogitorium: /usr/libexec/cogitorium/contextd is missing from this package — context will report unavailable" >&2
fi
exit 0
