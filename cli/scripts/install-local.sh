#!/bin/bash
set -e

# ============================================================
# install-local.sh - Build and install eigenflux CLI locally
# Usage: ./cli/scripts/install-local.sh
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$CLI_DIR/.."; pwd)"

source "$CLI_DIR/.cli.config"

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

INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"
cp "$PROJECT_ROOT/build/eigenflux" "$INSTALL_DIR/eigenflux"

# Ensure PATH includes install dir
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
  echo -e "${CYAN}Adding ${INSTALL_DIR} to PATH...${NC}"
  export PATH="$INSTALL_DIR:$PATH"
fi

echo -e "${GREEN}Installed: $("$INSTALL_DIR/eigenflux" version --short)${NC}"

# Migrate from OpenClaw if applicable
"$INSTALL_DIR/eigenflux" migrate 2>/dev/null || true
