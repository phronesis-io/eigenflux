#!/bin/bash
set -e

# ============================================================
# install-local.sh - Build and install eigenflux CLI locally
# Usage: ./cli/scripts/install-local.sh
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$CLI_DIR/.."; pwd)"

source "$CLI_DIR/CLI_CONFIG"

GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

echo -e "${CYAN}Building eigenflux CLI v${CLI_VERSION} for local platform...${NC}"

cd "$CLI_DIR"

# Prefer project-pinned Go via mise when available.
if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

"${GO_CMD[@]}" build -ldflags "-X main.Version=${CLI_VERSION}" -o "$PROJECT_ROOT/build/eigenflux" .

INSTALL_DIR="/usr/local/bin"
if [[ -w "$INSTALL_DIR" ]]; then
  cp "$PROJECT_ROOT/build/eigenflux" "$INSTALL_DIR/eigenflux"
else
  echo -e "${CYAN}Installing to $INSTALL_DIR (requires sudo)...${NC}"
  sudo cp "$PROJECT_ROOT/build/eigenflux" "$INSTALL_DIR/eigenflux"
fi

echo -e "${GREEN}Installed: $(eigenflux version --short)${NC}"
