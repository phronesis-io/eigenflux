#!/bin/bash
set -euo pipefail

# Usage: build-notifier-macos.sh <output-directory> <arm64|amd64>
# Output: <output-directory>/EigenFlux Notifications.app
if [[ $# != 2 || "$(uname -s)" != Darwin ]]; then
  echo 'usage (on macOS): build-notifier-macos.sh <output-directory> <arm64|amd64>' >&2
  exit 1
fi
case "$2" in
  arm64) architecture=arm64 ;;
  amd64) architecture=x86_64 ;;
  *) echo 'unsupported macOS architecture' >&2; exit 1 ;;
esac
script_dir="$(cd "$(dirname "$0")" && pwd)"
source_dir="$script_dir/../internal/desktopnotify/macos"
output_dir="$1/EigenFlux Notifications.app"
mkdir -p "$output_dir/Contents/MacOS"
cp "$source_dir/Info.plist" "$output_dir/Contents/Info.plist"
/usr/bin/xcrun swiftc -O -target "$architecture-apple-macosx11.0" \
  -module-cache-path "${TMPDIR:-/tmp}/eigenflux-swift-module-cache" \
  -framework AppKit -framework UserNotifications \
  "$source_dir/main.swift" -o "$output_dir/Contents/MacOS/EigenFluxNotifications"
# Ad-hoc signing is for locally built test bundles. Release builds provide a
# Developer ID identity and notarize the containing distributable separately.
/usr/bin/codesign --force --options runtime --sign "${EIGENFLUX_NOTIFIER_SIGN_IDENTITY:--}" "$output_dir"
/usr/bin/codesign --verify --deep --strict "$output_dir"
printf '%s\n' "$output_dir"
