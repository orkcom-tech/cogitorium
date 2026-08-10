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

# Cogitorium delegates context storage to Contextverse's contextd, which is a
# separate program from a separate repository. It cannot be a hard dependency:
# contextd ships from its own GitHub releases rather than from a distribution
# repository, so declaring Depends would make this package refuse to install on
# a machine with no way to satisfy it.
#
# So the package installs, and this says plainly what is missing. The server
# says the same thing at runtime — GET /api/v1/context/status — and the
# interface shows it, so nobody has to remember this message.
if command -v contextd >/dev/null 2>&1; then
    exit 0
fi

cat <<'MSG'

  Cogitorium is installed, but contextd was not found on this machine.

  Context and memory — an agent's own notes, shared workspace documents,
  everything it is given to read — are stored and versioned by Contextverse.
  Without contextd the server still starts and says so; memory does not work.

    Debian/Ubuntu:  see https://github.com/orkcom-tech/contextverse/releases
    Homebrew:       brew install orkcom-tech/tap/contextd
    From source:    https://github.com/orkcom-tech/contextverse

MSG
exit 0
