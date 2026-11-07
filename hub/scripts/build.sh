#!/usr/bin/env bash
# Build the FluxSpace Hub. Artifacts go to build/ per AGENTS.md.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p build
go build -o build/fluxspace-hub .
echo "built build/fluxspace-hub"
