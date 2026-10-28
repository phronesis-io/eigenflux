#!/bin/bash
set -euo pipefail
if [[ $# != 1 || "$(uname -s)" != Darwin ]]; then
  echo 'Production CLI publication requires macOS to verify signed, notarized notification packages.' >&2
  exit 1
fi
script_dir="$(cd "$(dirname "$0")" && pwd)"
python3 "$script_dir/cli-packages.py" verify "$1"
version=$(cat "$1/version.txt")
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
for arch in amd64 arm64; do
  python3 - "$1/eigenflux-darwin-$arch.zip" "$stage/$arch" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as archive:
    archive.extractall(sys.argv[2])
PY
  # Python zip extraction does not restore Unix mode bits.
  chmod +x "$stage/$arch/EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications"
  app="$stage/$arch/EigenFlux Notifications.app"
  for key in CFBundleVersion CFBundleShortVersionString; do
    [[ "$(/usr/libexec/PlistBuddy -c "Print :$key" "$app/Contents/Info.plist")" == "$version" ]] || {
      echo 'Notification helper version differs from its paired CLI release.' >&2; exit 1;
    }
  done
  /usr/bin/codesign --verify --deep --strict "$app"
  /usr/bin/codesign -d --verbose=4 "$app" 2>&1 | grep -q '^Authority=Developer ID Application:' || {
    echo 'Refusing ad-hoc or non-Developer-ID notification helper publication.' >&2; exit 1;
  }
  /usr/bin/xcrun stapler validate "$app"
  /usr/sbin/spctl --assess --type execute "$app"
done
