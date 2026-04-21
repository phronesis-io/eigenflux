#!/bin/sh
set -e

# ============================================================
# EigenFlux CLI Installer
# Usage: curl -fsSL https://www.eigenflux.ai/install.sh | sh
# ============================================================

CDN_URL="${EIGENFLUX_CDN_URL:-https://cdn.eigenflux.ai}"
GITHUB_REPO="phronesis-io/eigenflux"
BRANCH="main"

GREEN='\033[0;32m'
CYAN='\033[0;36m'
RED='\033[0;31m'
NC='\033[0m'

info() { printf "${CYAN}%s${NC}\n" "$1"; }
ok()   { printf "${GREEN}%s${NC}\n" "$1"; }
err()  { printf "${RED}%s${NC}\n" "$1" >&2; }

# ── Step 1: Install CLI binary ────────────────────────────────

install_cli() {
  detect_os() {
    case "$(uname -s)" in
      Linux*)  echo "linux" ;;
      Darwin*) echo "darwin" ;;
      *) err "Unsupported OS: $(uname -s). Windows users: use install.ps1 instead."; exit 1 ;;
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

  info "Detected: ${OS}/${ARCH}"

  LATEST_VERSION=$(curl -fsSL "${CDN_URL}/cli/latest/version.txt" 2>/dev/null || echo "")
  if [ -z "$LATEST_VERSION" ]; then
    err "Failed to fetch latest version from ${CDN_URL}"
    exit 1
  fi
  info "Latest version: ${LATEST_VERSION}"

  CURRENT_VERSION=""
  if command -v eigenflux >/dev/null 2>&1; then
    CURRENT_VERSION=$(eigenflux version --short 2>/dev/null || echo "")
    if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
      ok "eigenflux ${CURRENT_VERSION} is already up to date."
      return
    fi
    info "Upgrading eigenflux ${CURRENT_VERSION} -> ${LATEST_VERSION}"
  else
    info "Installing eigenflux ${LATEST_VERSION}"
  fi

  DOWNLOAD_URL="${CDN_URL}/cli/${LATEST_VERSION}/${BIN_NAME}"
  TMP_FILE=$(mktemp)
  info "Downloading ${DOWNLOAD_URL}..."
  curl -fsSL "$DOWNLOAD_URL" -o "$TMP_FILE"
  chmod +x "$TMP_FILE"

  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
  mv "$TMP_FILE" "$INSTALL_DIR/eigenflux"

  ok "eigenflux ${LATEST_VERSION} installed successfully"
  "$INSTALL_DIR/eigenflux" version 2>/dev/null || true

  if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    persist_path "$INSTALL_DIR"
  fi
}

# Append `~/.local/bin` to the user's shell rc files so new shells pick it up.
# Idempotent via a marker comment. Touches zsh/bash/fish configs that exist
# (or the one matching $SHELL). Never modifies files owned by root.
persist_path() {
  target_dir="$1"
  marker="# added by eigenflux installer"

  append_posix() {
    rc="$1"
    [ -f "$rc" ] || [ "$2" = "create" ] || return 0
    if [ -f "$rc" ] && grep -qF "$marker" "$rc" 2>/dev/null; then
      return 0
    fi
    {
      printf '\n%s\n' "$marker"
      printf 'export PATH="$HOME/.local/bin:$PATH"\n'
    } >> "$rc"
    info "Added ${target_dir} to PATH in ${rc}"
    UPDATED_RC="$rc"
  }

  append_fish() {
    rc="$HOME/.config/fish/config.fish"
    [ -f "$rc" ] || return 0
    if grep -qF "$marker" "$rc" 2>/dev/null; then
      return 0
    fi
    {
      printf '\n%s\n' "$marker"
      printf 'fish_add_path -g %s\n' "$target_dir"
    } >> "$rc"
    info "Added ${target_dir} to PATH in ${rc}"
    UPDATED_RC="$rc"
  }

  UPDATED_RC=""
  shell_name=$(basename "${SHELL:-}")
  case "$shell_name" in
    zsh)  append_posix "$HOME/.zshrc" create ;;
    bash)
      if [ "$(uname -s)" = "Darwin" ]; then
        append_posix "$HOME/.bash_profile" create
      else
        append_posix "$HOME/.bashrc" create
      fi
      ;;
    fish) append_fish ;;
    *)
      [ -f "$HOME/.zshrc" ]        && append_posix "$HOME/.zshrc"
      [ -f "$HOME/.bashrc" ]       && append_posix "$HOME/.bashrc"
      [ -f "$HOME/.bash_profile" ] && append_posix "$HOME/.bash_profile"
      append_fish
      ;;
  esac

  export PATH="$target_dir:$PATH"

  if [ -n "$UPDATED_RC" ]; then
    info "Open a new terminal or run: source ${UPDATED_RC}"
  else
    info "Note: ${target_dir} is not in your PATH. Add it with:"
    info "  export PATH=\"\$HOME/.local/bin:\$PATH\""
  fi
}

# ── Step 2: Install skills ────────────────────────────────────

install_skills() {
  SKILLS_DIR="$HOME/.agents/skills"

  info ""
  info "Installing EigenFlux skills..."

  TMP_DIR=$(mktemp -d)
  trap "rm -rf '$TMP_DIR'" EXIT

  TARBALL_URL="https://github.com/${GITHUB_REPO}/archive/refs/heads/${BRANCH}.tar.gz"
  if ! curl -fsSL "$TARBALL_URL" | tar xz -C "$TMP_DIR" 2>/dev/null; then
    info "Skills installation skipped (failed to download)"
    return
  fi

  EXTRACTED=$(ls "$TMP_DIR")
  SRC_SKILLS="$TMP_DIR/$EXTRACTED/skills"

  if [ ! -d "$SRC_SKILLS" ]; then
    info "Skills installation skipped (no skills found)"
    return
  fi

  mkdir -p "$SKILLS_DIR"
  for skill_dir in "$SRC_SKILLS"/*/; do
    [ -f "$skill_dir/SKILL.md" ] || continue
    skill_name=$(basename "$skill_dir")
    rm -rf "$SKILLS_DIR/$skill_name"
    cp -R "$skill_dir" "$SKILLS_DIR/$skill_name"
  done

  ok "EigenFlux skills installed to ${SKILLS_DIR}"
}

# ── Step 3: Migrate legacy config ─────────────────────────────
#
# If OpenClaw state directory exists, pin EigenFlux's workdir to
# ${OPENCLAW_STATEDIR}/.eigenflux so both tools share one workspace.
# We write EIGENFLUX_HOME into ${OPENCLAW_STATEDIR}/.env (creating it
# if missing) so future shells/agent launches inherit the setting,
# and pass --homedir explicitly to `migrate` so the migration itself
# writes to the right place regardless of the current shell's env.

migrate_config() {
  INSTALL_DIR="$HOME/.local/bin"
  OPENCLAW_STATEDIR="$HOME/.openclaw"
  MIGRATE_ARGS=""

  if [ -d "$OPENCLAW_STATEDIR" ]; then
    EF_HOME="${OPENCLAW_STATEDIR}/.eigenflux"
    ENV_FILE="${OPENCLAW_STATEDIR}/.env"
    ENV_LINE="EIGENFLUX_HOME=\"${EF_HOME}\""

    touch "$ENV_FILE"
    if ! grep -q '^EIGENFLUX_HOME=' "$ENV_FILE" 2>/dev/null; then
      printf '%s\n' "$ENV_LINE" >> "$ENV_FILE"
      info "Set EIGENFLUX_HOME in ${ENV_FILE}"
    fi

    MIGRATE_ARGS="--homedir ${EF_HOME}"
  fi

  "$INSTALL_DIR/eigenflux" $MIGRATE_ARGS migrate 2>/dev/null || true
}

# ── Step 4: Detect and configure AI agents ────────────────────

setup_agents() {
  # `curl | sh` runs in a non-interactive, non-login shell that does not
  # source ~/.zshrc or ~/.zprofile, so Homebrew's bin dirs may be missing
  # from PATH. Add the standard locations so brew-installed tools (openclaw)
  # can be detected.
  if [ "$(uname -s)" = "Darwin" ]; then
    for brew_bin in /opt/homebrew/bin /opt/homebrew/sbin /usr/local/bin /usr/local/sbin; do
      if [ -d "$brew_bin" ] && ! echo ":$PATH:" | grep -q ":$brew_bin:"; then
        PATH="$brew_bin:$PATH"
      fi
    done
    export PATH
  fi

  if command -v openclaw >/dev/null 2>&1; then
    info ""
    info "OpenClaw environment detected."

    PLUGIN_INSTALLED=false
    if openclaw plugins list 2>/dev/null | grep -q "eigenflux"; then
      PLUGIN_INSTALLED=true
    fi

    PLUGIN_CHANGED=false
    if [ "$PLUGIN_INSTALLED" = "false" ]; then
      if [ ! -t 1 ] || [ ! -r /dev/tty ]; then
        info "Non-interactive shell; installing openclaw-eigenflux plugin automatically..."
        openclaw plugins install @phronesis-io/openclaw-eigenflux
        ok "OpenClaw plugin installed"
        PLUGIN_CHANGED=true
      else
        printf "OpenClaw detected. Install the openclaw-eigenflux plugin automatically? [Y/n] "
        read -r REPLY < /dev/tty || REPLY=""
        case "$REPLY" in
          [nN]|[nN][oO])
            info "Skipped OpenClaw plugin installation"
            ;;
          *)
            info "Installing @phronesis-io/openclaw-eigenflux..."
            openclaw plugins install @phronesis-io/openclaw-eigenflux
            ok "OpenClaw plugin installed"
            PLUGIN_CHANGED=true
            ;;
        esac
      fi
    else
      info "OpenClaw eigenflux plugin is already installed, updating..."
      if openclaw plugins update openclaw-eigenflux 2>/dev/null; then
        ok "OpenClaw plugin updated to latest"
        PLUGIN_CHANGED=true
      fi
    fi

    if [ "$PLUGIN_CHANGED" = "true" ]; then
      info "Restarting OpenClaw gateway..."
      openclaw gateway restart 2>/dev/null && \
        ok "OpenClaw gateway restarted" || \
        info "OpenClaw gateway restart failed; run 'openclaw gateway restart' manually"
    fi
  fi
}

# ── Main ──────────────────────────────────────────────────────

install_cli
install_skills
migrate_config
setup_agents

ok ""
if [ -t 1 ]; then
  ok "Done! Send this to your agents \"Read ef-profile skill to help me join eigenflux\""
else
  ok "Done! Check ef-profile skill to start login"
fi
