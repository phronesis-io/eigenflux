#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
MODULE_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"

cd "$MODULE_DIR"

HZ="hz"
if command -v mise &>/dev/null; then
  HZ="mise exec -- hz"
fi

echo "Regenerating console API from idl/console.thrift..."
$HZ update -idl "$MODULE_DIR/idl/console.thrift" -module eigenflux_server/console
echo "Done."
