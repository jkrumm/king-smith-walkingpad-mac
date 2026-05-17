#!/usr/bin/env bash
# Wrap a compiled king-smith-walkingpad-mac binary into a macOS .app bundle.
#
# Usage:  scripts/build-app-bundle.sh <binary-path> <version> <output-app-path>
#
# Why this script exists: macOS CoreBluetooth silently denies bare CLI binaries.
# The daemon must run from a .app bundle whose Info.plist declares the
# NSBluetoothAlwaysUsageDescription + NSBluetoothPeripheralUsageDescription
# strings. See PRD §12.
#
# The bundle is unsigned in v1 (Gatekeeper requires right-click → Open the first
# time; users are walked through this in the README).

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <binary> <version> <output.app>" >&2
  exit 64 # EX_USAGE
fi

BINARY=$1
VERSION=$2
APP_PATH=$3

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PLIST_TMPL="$SCRIPT_DIR/Info.plist.tmpl"

if [[ ! -x "$BINARY" ]]; then
  echo "error: binary not found or not executable: $BINARY" >&2
  exit 1
fi
if [[ ! -f "$PLIST_TMPL" ]]; then
  echo "error: Info.plist template missing: $PLIST_TMPL" >&2
  exit 1
fi

BINARY_BASENAME=$(basename "$BINARY")
APP_DIR="$APP_PATH/Contents"

# Remove any previous bundle so stale Info.plist version strings don't linger.
rm -rf "$APP_PATH"
mkdir -p "$APP_DIR/MacOS" "$APP_DIR/Resources"

# Substitute __VERSION__ into the Info.plist. Use a temp file so the template
# stays read-only on disk.
sed "s/__VERSION__/${VERSION//\//_}/g" "$PLIST_TMPL" > "$APP_DIR/Info.plist"

# Copy the binary; preserve the executable bit. Use install to set 0755 explicitly.
install -m 0755 "$BINARY" "$APP_DIR/MacOS/$BINARY_BASENAME"

# Strip any quarantine xattrs that would block CoreBluetooth TCC. Safe to ignore
# the exit code — if there are no xattrs there is nothing to strip.
xattr -cr "$APP_PATH" 2>/dev/null || true

# Ad-hoc sign the bundle. macOS Sequoia+ TCC matches Bluetooth permission against
# the bundle's signature; an unsigned bundle's identity is fragile (rotates on
# every cp -R) and TCC will silently deny scans even when the toggle is on in
# System Settings. Ad-hoc signature (--sign -) is enough to give the bundle a
# stable Designated Requirement on this machine; proper notarisation is a v2 item.
if ! codesign --force --deep --sign - "$APP_PATH" 2>/tmp/codesign.err; then
  echo "codesign failed (continuing — Bluetooth permission may not stick):" >&2
  cat /tmp/codesign.err >&2
fi

# Touch the bundle so Launch Services notices the change next time it's opened.
touch "$APP_PATH"

echo "wrote $APP_PATH (v$VERSION)"
