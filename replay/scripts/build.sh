#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

GO_CMD="go"
if command -v mise &>/dev/null; then
  GO_CMD="mise exec -- go"
fi

mkdir -p build
$GO_CMD build -o build/replay ./replay/
echo "Built: build/replay"
