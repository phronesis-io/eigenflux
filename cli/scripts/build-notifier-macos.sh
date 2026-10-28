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
if [[ -z "${EIGENFLUX_NOTIFIER_VERSION:-}" ]]; then
  source "$script_dir/../.cli.config"
  EIGENFLUX_NOTIFIER_VERSION="$CLI_VERSION"
fi
if [[ ! "$EIGENFLUX_NOTIFIER_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo 'Notification helper version must be a stable CLI semantic version.' >&2
  exit 1
fi
output_dir="$1/EigenFlux Notifications.app"
mkdir -p "$output_dir/Contents/MacOS" "$output_dir/Contents/Resources"
cp "$source_dir/Info.plist" "$output_dir/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $EIGENFLUX_NOTIFIER_VERSION" "$output_dir/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $EIGENFLUX_NOTIFIER_VERSION" "$output_dir/Contents/Info.plist"
icon_work="$(mktemp -d "${TMPDIR:-/tmp}/eigenflux-icon.XXXXXX")"
trap 'rm -rf "$icon_work"' EXIT
mkdir "$icon_work/EigenFlux.iconset"
for size in 16 32 128 256 512; do
  /usr/bin/sips -z "$size" "$size" "$source_dir/Icon.png" \
    --out "$icon_work/EigenFlux.iconset/icon_${size}x${size}.png" >/dev/null
  /usr/bin/sips -z "$((size * 2))" "$((size * 2))" "$source_dir/Icon.png" \
    --out "$icon_work/EigenFlux.iconset/icon_${size}x${size}@2x.png" >/dev/null
done
/usr/bin/iconutil -c icns "$icon_work/EigenFlux.iconset" \
  -o "$output_dir/Contents/Resources/EigenFlux.icns"
/usr/bin/xcrun swiftc -O -target "$architecture-apple-macosx11.0" \
  -module-cache-path "${TMPDIR:-/tmp}/eigenflux-swift-module-cache" \
  -framework AppKit -framework UserNotifications \
  "$source_dir/main.swift" -o "$output_dir/Contents/MacOS/EigenFluxNotifications"
# Ad-hoc signing is for locally built test bundles. Release builds provide a
# Developer ID identity and notarize the containing distributable separately.
signing_args=(--force --options runtime --sign "${EIGENFLUX_NOTIFIER_SIGN_IDENTITY:--}")
if [[ "${EIGENFLUX_NOTIFIER_SIGN_IDENTITY:--}" != - ]]; then
  signing_args+=(--timestamp)
fi
/usr/bin/codesign "${signing_args[@]}" "$output_dir"
/usr/bin/codesign --verify --deep --strict "$output_dir"
printf '%s\n' "$output_dir"
