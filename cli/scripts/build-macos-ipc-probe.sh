#!/bin/bash
# Package the opt-in, read-only host probe for Macs without Go.
set -euo pipefail
cd "$(dirname "$0")/.."
out=../build/macos-ipc-probe
mkdir -p "$out"
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go test -trimpath -c ./internal/dispatch -o "$out/probe-$arch"
done
lipo -create "$out/probe-arm64" "$out/probe-amd64" -output "$out/ipc-probe"
codesign --force --sign - "$out/ipc-probe"
tar -czf "$out/macos-ipc-probe.tar.gz" -C "$out" ipc-probe
