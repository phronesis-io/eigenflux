#!/bin/bash
set -e

# ============================================================
# build.sh - Compile all microservices to build/ directory
# Usage: ./scripts/common/build.sh [service_name...]
#   No arguments: Compile all services
#   Specify services: ./scripts/common/build.sh profile item sort feed api pipeline
#   Console is an independent subsystem: ./console/console_api/scripts/build.sh
# ============================================================

PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

# name:source mapping
ALL_SERVICES=(
  "profile:./rpc/profile/"
  "item:./rpc/item/"
  "sort:./rpc/sort/"
  "feed:./rpc/feed/"
  "pm:./rpc/pm/"
  "auth:./rpc/auth/"
  "notification:./rpc/notification/"
  "api:./api/"
  "ws:./ws/"
  "pipeline:./pipeline/"
  "cron:./pipeline/cron/"
)

get_source() {
  local target=$1
  for entry in "${ALL_SERVICES[@]}"; do
    IFS=':' read -r name src <<< "$entry"
    if [[ "$name" == "$target" ]]; then
      echo "$src"
      return 0
    fi
  done
  return 1
}

# If arguments are specified, only compile specified services
targets=("$@")
if [[ ${#targets[@]} -eq 0 ]]; then
  for entry in "${ALL_SERVICES[@]}"; do
    IFS=':' read -r name _ <<< "$entry"
    targets+=("$name")
  done
fi

mkdir -p "$BUILD_DIR"
cd "$PROJECT_ROOT"

# Regenerate Swagger docs before building
echo -e "${CYAN}Regenerating Swagger documentation...${NC}"
SWAG_CMD="$(go env GOPATH)/bin/swag"
if [[ -x "$SWAG_CMD" ]]; then
  # API gateway swagger (run from api directory to avoid path resolution issues)
  (cd api && "$SWAG_CMD" init -g main.go -o docs --parseDependency --exclude ../console >/dev/null 2>&1) && \
    echo -e "${GREEN}✓ API gateway swagger${NC}" || echo -e "${RED}✗ API gateway swagger${NC}"
else
  echo -e "${RED}swag not installed, skipping Swagger documentation generation${NC}"
fi
echo ""

# Prefer project-pinned Go via mise when available.
if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

failed=0
for name in "${targets[@]}"; do
  src=$(get_source "$name") || {
    echo -e "${RED}Unknown service: $name${NC}"
    echo "Available services: profile item sort feed pm auth notification api pipeline cron"
    exit 1
  }
  echo -ne "${CYAN}Compiling $name ...${NC} "
  if "${GO_CMD[@]}" build -o "$BUILD_DIR/$name" "$src" 2>&1; then
    echo -e "${GREEN}OK${NC}"
  else
    echo -e "${RED}FAILED${NC}"
    failed=1
  fi
done

if [[ $failed -eq 0 ]]; then
  echo -e "\n${GREEN}All services compiled → build/${NC}"
else
  echo -e "\n${RED}Some services failed to compile${NC}"
  exit 1
fi
