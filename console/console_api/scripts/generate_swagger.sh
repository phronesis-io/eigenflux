#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
MODULE_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"

SWAG="swag"
if command -v mise &>/dev/null; then
  SWAG="mise exec -- swag"
fi

echo "Regenerating console Swagger docs..."
$SWAG init -g main.go -o docs --parseDependency -d "$MODULE_DIR"
echo "Done."
