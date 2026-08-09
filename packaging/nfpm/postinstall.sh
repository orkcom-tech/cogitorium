#!/bin/sh
# Cogitorium delegates context storage to Contextverse's contextd, which is a
# separate program from a separate repository. It cannot be a hard dependency:
# contextd ships from its own GitHub releases rather than from a distribution
# repository, so declaring Depends would make this package refuse to install on
# a machine with no way to satisfy it.
#
# So the package installs, and this says plainly what is missing. The server
# says the same thing at runtime — GET /api/v1/context/status — and the
# interface shows it, so nobody has to remember this message.
set -e

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
