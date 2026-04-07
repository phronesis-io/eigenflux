#!/bin/bash
set -e
PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"

if [[ -f "$PROJECT_ROOT/.env" ]]; then
  set -a
  source "$PROJECT_ROOT/.env"
  set +a
fi

"$BUILD_DIR/ws"
