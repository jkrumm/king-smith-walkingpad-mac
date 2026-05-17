#!/usr/bin/env bash
# Generate a multi-resolution .icns from a single square PNG.
#
# Usage:  scripts/build-icns.sh <input.png> <output.icns>
#
# Uses macOS built-ins (sips + iconutil), so no third-party deps. Source PNG
# should be at least 512×512; the 512@2x slot (1024px) is sips-upscaled so
# very-large Finder previews look slightly soft but still correct. Re-export
# a 1024×1024 source if that ever matters.

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <input.png> <output.icns>" >&2
  exit 64
fi

SRC=$1
OUT=$2

if [[ ! -f "$SRC" ]]; then
  echo "error: source PNG not found: $SRC" >&2
  exit 1
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
ICONSET="$TMP/AppIcon.iconset"
mkdir -p "$ICONSET"

# Standard Apple iconset sizes — sips silently rounds odd inputs.
sips -z 16   16   "$SRC" --out "$ICONSET/icon_16x16.png"        >/dev/null
sips -z 32   32   "$SRC" --out "$ICONSET/icon_16x16@2x.png"     >/dev/null
sips -z 32   32   "$SRC" --out "$ICONSET/icon_32x32.png"        >/dev/null
sips -z 64   64   "$SRC" --out "$ICONSET/icon_32x32@2x.png"     >/dev/null
sips -z 128  128  "$SRC" --out "$ICONSET/icon_128x128.png"      >/dev/null
sips -z 256  256  "$SRC" --out "$ICONSET/icon_128x128@2x.png"   >/dev/null
sips -z 256  256  "$SRC" --out "$ICONSET/icon_256x256.png"      >/dev/null
sips -z 512  512  "$SRC" --out "$ICONSET/icon_256x256@2x.png"   >/dev/null
sips -z 512  512  "$SRC" --out "$ICONSET/icon_512x512.png"      >/dev/null
sips -z 1024 1024 "$SRC" --out "$ICONSET/icon_512x512@2x.png"   >/dev/null

iconutil -c icns "$ICONSET" -o "$OUT"
echo "wrote $OUT"
