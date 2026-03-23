#!/bin/bash
set -e

# ============================================================
# build.sh - Compile console API service
# Usage: ./console/console_api/scripts/build.sh
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
MODULE_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$MODULE_DIR/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

mkdir -p "$BUILD_DIR"

# Regenerate Swagger docs
bash "$SCRIPT_DIR/generate_swagger.sh"

# Prefer project-pinned Go via mise when available.
if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

echo -ne "${CYAN}Compiling console ...${NC} "
if (cd "$MODULE_DIR" && "${GO_CMD[@]}" build -o "$BUILD_DIR/console" .) 2>&1; then
  echo -e "${GREEN}OK${NC}"
else
  echo -e "${RED}FAILED${NC}"
  exit 1
fi

echo -e "${GREEN}Console compiled → build/console${NC}"
