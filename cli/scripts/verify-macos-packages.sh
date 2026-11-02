#!/bin/bash
set -euo pipefail
if [[ $# != 1 || "$(uname -s)" != Darwin ]]; then
  echo 'Production CLI publication requires macOS to verify signed, notarized notification packages.' >&2
  exit 1
fi
if [[ ! "${APPLE_TEAM_ID:-}" =~ ^[A-Z0-9]{10}$ ]]; then
  echo 'APPLE_TEAM_ID is required for local notification package identity validation.' >&2
  exit 1
fi
script_dir="$(cd "$(dirname "$0")" && pwd)"
python3 "$script_dir/cli-packages.py" verify "$1"
version=$(python3 - "$1" <<'PYVERSION'
import json, pathlib, sys
root=pathlib.Path(sys.argv[1])
p=root/'package-release-v2.json'
if p.exists():
    manifest=json.loads(p.read_text())
    print(manifest['notifier_version'])
else:
    print((root/'version.txt').read_text().strip())
PYVERSION
)
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
      echo 'Notification helper version differs from its signed manifest.' >&2; exit 1;
    }
  done
  /usr/bin/codesign --verify --deep --strict "$app"
  identity=$(/usr/bin/codesign -d --verbose=4 "$app" 2>&1)
  grep -q '^Authority=Developer ID Application:' <<< "$identity" || {
    echo 'Refusing ad-hoc or non-Developer-ID notification helper publication.' >&2; exit 1;
  }
  grep -Fxq "TeamIdentifier=$APPLE_TEAM_ID" <<< "$identity" || {
    echo 'Notification helper belongs to another Apple team.' >&2; exit 1;
  }
  # The immutable notifier release has already passed online ticket and
  # Gatekeeper verification. CLI assembly verifies the exact signed package
  # locally; it must not re-query Apple for an unchanged helper.
done
