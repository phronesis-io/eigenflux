#!/bin/bash
set -e

# ============================================================
# build.sh - Cross-compile eigenflux CLI for all platforms
# Usage: ./cli/scripts/build.sh
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$CLI_DIR/.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build/cli"

source "$CLI_DIR/.cli.config"

CLI_COMMIT=$(git -C "$PROJECT_ROOT" rev-parse --short=8 HEAD 2>/dev/null || echo "unknown")

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

# Prefer project-pinned Go via mise when available.
if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

echo -e "${CYAN}Building eigenflux CLI v${CLI_VERSION} (commit ${CLI_COMMIT})${NC}"
echo ""

failed=0
cd "$CLI_DIR"

for platform in "${PLATFORMS[@]}"; do
  IFS='/' read -r os arch <<< "$platform"
  bin_name="eigenflux-${os}-${arch}"
  if [[ "$os" == "windows" ]]; then
    bin_name="${bin_name}.exe"
  fi

  echo -ne "${CYAN}Compiling ${os}/${arch} ...${NC} "
  if GOOS="$os" GOARCH="$arch" "${GO_CMD[@]}" build \
    -ldflags "-X main.Version=${CLI_VERSION} -X main.Commit=${CLI_COMMIT}" \
    -o "$BUILD_DIR/$bin_name" . 2>&1; then
    echo -e "${GREEN}OK${NC}"
  else
    echo -e "${RED}FAILED${NC}"
    failed=1
  fi
done

# Write version file for install.sh
echo "$CLI_VERSION" > "$BUILD_DIR/version.txt"

echo ""
if [[ $failed -eq 0 ]]; then
  echo -e "${GREEN}All platforms compiled → build/cli/${NC}"
  ls -lh "$BUILD_DIR"
else
  echo -e "${RED}Some platforms failed to compile${NC}"
  exit 1
fi
