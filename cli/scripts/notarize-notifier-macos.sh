#!/bin/bash
set -euo pipefail
if [[ $# != 1 || "$(uname -s)" != Darwin || -z "${EIGENFLUX_NOTARY_PROFILE:-}" ]]; then
  echo 'usage (macOS): EIGENFLUX_NOTARY_PROFILE=<keychain profile> notarize-notifier-macos.sh <helpers-directory>' >&2
  exit 1
fi
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
for arch in amd64 arm64; do
  app="$1/darwin-$arch/EigenFlux Notifications.app"
  /usr/bin/codesign --verify --deep --strict "$app"
  /usr/bin/codesign -d --verbose=4 "$app" 2>&1 | grep -q '^Authority=Developer ID Application:' || {
    echo 'Production notification helpers require a Developer ID Application signature.' >&2; exit 1;
  }
  /usr/bin/ditto -c -k --keepParent "$app" "$stage/notifier-$arch.zip"
  /usr/bin/xcrun notarytool submit "$stage/notifier-$arch.zip" --keychain-profile "$EIGENFLUX_NOTARY_PROFILE" --wait --timeout 20m
  /usr/bin/xcrun stapler staple "$app"
  /usr/bin/xcrun stapler validate "$app"
  /usr/sbin/spctl --assess --type execute "$app"
done
