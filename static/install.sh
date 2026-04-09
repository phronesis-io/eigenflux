#!/bin/sh
set -e

# ============================================================
# EigenFlux CLI Installer
# Usage: curl -fsSL https://www.eigenflux.ai/install.sh | sh
# ============================================================

CDN_URL="${EIGENFLUX_CDN_URL:-https://cdn.eigenflux.ai}"

GREEN='\033[0;32m'
CYAN='\033[0;36m'
RED='\033[0;31m'
NC='\033[0m'

info() { printf "${CYAN}%s${NC}\n" "$1"; }
ok() { printf "${GREEN}%s${NC}\n" "$1"; }
err() { printf "${RED}%s${NC}\n" "$1" >&2; }

detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *) err "Unsupported OS: $(uname -s)"; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
  esac
}

OS=$(detect_os)
ARCH=$(detect_arch)
BIN_NAME="eigenflux-${OS}-${ARCH}"
if [ "$OS" = "windows" ]; then
  BIN_NAME="${BIN_NAME}.exe"
fi

info "Detected: ${OS}/${ARCH}"

# Fetch latest version
LATEST_VERSION=$(curl -fsSL "${CDN_URL}/cli/latest/version.txt" 2>/dev/null || echo "")
if [ -z "$LATEST_VERSION" ]; then
  err "Failed to fetch latest version from ${CDN_URL}"
  exit 1
fi
info "Latest version: ${LATEST_VERSION}"

# Check if already installed
CURRENT_VERSION=""
if command -v eigenflux >/dev/null 2>&1; then
  CURRENT_VERSION=$(eigenflux version --short 2>/dev/null || echo "")
  if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
    ok "eigenflux ${CURRENT_VERSION} is already up to date."
  else
    info "Upgrading eigenflux ${CURRENT_VERSION} -> ${LATEST_VERSION}"
  fi
else
  info "Installing eigenflux ${LATEST_VERSION}"
fi

if [ "$CURRENT_VERSION" != "$LATEST_VERSION" ]; then
  DOWNLOAD_URL="${CDN_URL}/cli/${LATEST_VERSION}/${BIN_NAME}"
  TMP_FILE=$(mktemp)
  info "Downloading ${DOWNLOAD_URL}..."
  curl -fsSL "$DOWNLOAD_URL" -o "$TMP_FILE"
  chmod +x "$TMP_FILE"

  # Install to ~/.local/bin (standard user binary dir, no sudo needed)
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
  mv "$TMP_FILE" "$INSTALL_DIR/eigenflux"

  ok "eigenflux ${LATEST_VERSION} installed successfully"
  "$INSTALL_DIR/eigenflux" version 2>/dev/null || true

  if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    info "Note: ${INSTALL_DIR} is not in your PATH. Add it with:"
    info "  export PATH=\"\$HOME/.local/bin:\$PATH\""
  fi
fi

# Migrate from OpenClaw plugin config (if applicable)
"$INSTALL_DIR/eigenflux" migrate 2>/dev/null || true

# Detect OpenClaw
if command -v openclaw >/dev/null 2>&1; then
  info ""
  info "OpenClaw environment detected."

  PLUGIN_INSTALLED=false
  if openclaw plugins list 2>/dev/null | grep -q "eigenflux"; then
    PLUGIN_INSTALLED=true
  fi

  if [ "$PLUGIN_INSTALLED" = "false" ]; then
    printf "Install the eigenflux OpenClaw plugin? [y/N] "
    read -r REPLY
    case "$REPLY" in
      [yY]|[yY][eE][sS])
        info "Installing @phronesis-io/openclaw-eigenflux..."
        openclaw plugins install @phronesis-io/openclaw-eigenflux
        ok "OpenClaw plugin installed"
        ;;
      *)
        info "Skipped OpenClaw plugin installation"
        ;;
    esac
  else
    info "OpenClaw eigenflux plugin is already installed"
    # Check for updates
    openclaw plugins install @phronesis-io/openclaw-eigenflux 2>/dev/null && \
      ok "OpenClaw plugin updated to latest" || true
  fi
fi

ok ""
ok "Done! Run 'eigenflux --help' to get started."
