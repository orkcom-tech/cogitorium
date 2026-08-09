#!/usr/bin/env sh
# Put Cogitorium where a Linux desktop will find it.
#
# Per-user by default: no root, nothing outside ~/.local, and everything it
# writes is listed below so removing it is obvious. Pass --system to install
# for everyone instead.
set -eu

PREFIX="${HOME}/.local"
if [ "${1:-}" = "--system" ]; then
    PREFIX="/usr/local"
fi

BIN="$PREFIX/bin"
APPS="$PREFIX/share/applications"
ICONS="$PREFIX/share/icons/hicolor/512x512/apps"

mkdir -p "$BIN" "$APPS" "$ICONS"
install -m 0755 cogitorium-desktop "$BIN/cogitorium-desktop"
install -m 0644 cogitorium.desktop "$APPS/cogitorium.desktop"
install -m 0644 cogitorium.png "$ICONS/cogitorium.png"

# Menus cache their entries; without this the application appears after the
# next login rather than now, which reads as a failed install.
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$APPS" || true
command -v gtk-update-icon-cache   >/dev/null 2>&1 && gtk-update-icon-cache -f -t "$PREFIX/share/icons/hicolor" || true

cat <<MSG

  Installed:
    $BIN/cogitorium-desktop
    $APPS/cogitorium.desktop
    $ICONS/cogitorium.png

  Launch it from your application menu, or run: cogitorium-desktop
MSG

if [ "$PREFIX" = "${HOME}/.local" ] && ! printf '%s' ":$PATH:" | grep -q ":$BIN:"; then
    echo "  Note: $BIN is not on your PATH, so only the menu entry will work until it is."
fi
