#!/bin/sh
set -e

# ============================================================
# EigenFlux CLI Installer
# Usage: curl -fsSL https://www.eigenflux.ai/install.sh | sh
#        curl -fsSL https://www.eigenflux.ai/install.sh | sh -s -- --ref EF-xxxxxxxx
# ============================================================

CDN_URL="${EIGENFLUX_CDN_URL:-https://cdn.eigenflux.ai}"
# Site origin for the install-attribution report (separate from the binary CDN).
EIGENFLUX_API_URL="${EIGENFLUX_API_URL:-https://www.eigenflux.ai}"
GITHUB_REPO="phronesis-io/eigenflux"
BRANCH="main"

GREEN='\033[0;32m'
CYAN='\033[0;36m'
RED='\033[0;31m'
NC='\033[0m'

info() { printf "${CYAN}%s${NC}\n" "$1"; }
ok()   { printf "${GREEN}%s${NC}\n" "$1"; }
err()  { printf "${RED}%s${NC}\n" "$1" >&2; }

# Optional referral code from the /install landing page (attributes this install
# to its ad campaign). Parsed up front; unknown args are ignored so existing
# `curl | sh` invocations are unaffected.
INSTALL_REF=""
while [ $# -gt 0 ]; do
  case "$1" in
    --ref)
      INSTALL_REF="${2:-}"
      shift
      [ $# -gt 0 ] && shift
      ;;
    --ref=*) INSTALL_REF="${1#*=}"; shift ;;
    --help|-h)
      printf 'Usage: curl -fsSL %s/install.sh | sh -s -- [options]\n\n' "$EIGENFLUX_API_URL"
      printf '  --ref EF-xxxxxxxx   Referral code from the /install page (optional)\n'
      printf '  --help             Show this help\n'
      exit 0
      ;;
    *) shift ;;
  esac
done

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

  # Honor EIGENFLUX_INSTALL_DIR override; default to ~/.local/bin.
  INSTALL_DIR="${EIGENFLUX_INSTALL_DIR:-$HOME/.local/bin}"
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
      printf 'export PATH="%s:$PATH"\n' "$target_dir"
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
    info "  export PATH=\"${target_dir}:\$PATH\""
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
  INSTALL_DIR="${EIGENFLUX_INSTALL_DIR:-$HOME/.local/bin}"
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
    # Iterate from lowest to highest priority: each iteration prepends, so
    # the last one iterated ends up at the front of PATH. This makes
    # /opt/homebrew/bin win on Apple Silicon where both trees may exist.
    for brew_bin in /usr/local/sbin /usr/local/bin /opt/homebrew/sbin /opt/homebrew/bin; do
      if [ -d "$brew_bin" ] && ! echo ":$PATH:" | grep -Fq ":$brew_bin:"; then
        PATH="$brew_bin:$PATH"
      fi
    done
    export PATH
  fi

  if command -v openclaw >/dev/null 2>&1; then
    info ""
    info "OpenClaw environment detected."

    # Determine the plugin specifier based on OpenClaw version.
    # >= 2026.5.2 uses latest; 2026.3.x–2026.5.1 pins @0.0.8.
    # Override with OPENCLAW_VERSION env var when auto-detection is unreliable
    # (e.g. non-interactive shells, CI, agent-driven installs).
    if [ -n "${OPENCLAW_VERSION:-}" ]; then
      OC_VERSION="$OPENCLAW_VERSION"
      info "Using OPENCLAW_VERSION from environment: ${OC_VERSION}"
    else
      OC_RAW=$(openclaw --version 2>&1 || true)
      OC_VERSION=$(printf '%s' "$OC_RAW" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    fi
    PLUGIN_SPEC="@phronesis-io/openclaw-eigenflux"
    if [ -n "$OC_VERSION" ]; then
      OC_MAJOR=$(echo "$OC_VERSION" | cut -d. -f1)
      OC_MINOR=$(echo "$OC_VERSION" | cut -d. -f2)
      OC_PATCH=$(echo "$OC_VERSION" | cut -d. -f3)
      if [ "$OC_MAJOR" = "2026" ]; then
        if [ "$OC_MINOR" -lt 3 ] 2>/dev/null; then
          err "OpenClaw ${OC_VERSION} is too old; please upgrade to 2026.3.0 or later."
          return
        elif [ "$OC_MINOR" -lt 5 ] 2>/dev/null || { [ "$OC_MINOR" = "5" ] && [ "${OC_PATCH:-0}" -lt 2 ] 2>/dev/null; }; then
          PLUGIN_SPEC="@phronesis-io/openclaw-eigenflux@0.0.8"
        fi
      fi
      info "OpenClaw version: ${OC_VERSION} -> plugin: ${PLUGIN_SPEC}"
    else
      info "Could not detect OpenClaw version; installing latest plugin"
    fi

    install_openclaw_plugin() {
      spec="$1"
      if [ "$PLUGIN_INSTALLED" = "true" ] && [ "$spec" != "@phronesis-io/openclaw-eigenflux" ]; then
        info "Reinstalling OpenClaw plugin with ${spec}..."
        openclaw plugins uninstall openclaw-eigenflux --force >/dev/null 2>&1 || true
        openclaw plugins install "$spec"
      elif [ "$PLUGIN_INSTALLED" = "true" ]; then
        info "Updating OpenClaw plugin to latest..."
        openclaw plugins update openclaw-eigenflux 2>/dev/null || openclaw plugins install "$spec"
      else
        openclaw plugins install "$spec"
      fi
    }

    PLUGIN_INSTALLED=false
    if openclaw plugins list 2>/dev/null | grep -q "eigenflux"; then
      PLUGIN_INSTALLED=true
    fi

    PLUGIN_CHANGED=false
    if [ "$PLUGIN_INSTALLED" = "false" ]; then
      if [ ! -t 1 ] || [ ! -r /dev/tty ]; then
        info "Non-interactive shell; installing openclaw-eigenflux plugin automatically..."
        install_openclaw_plugin "$PLUGIN_SPEC"
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
            info "Installing ${PLUGIN_SPEC}..."
            install_openclaw_plugin "$PLUGIN_SPEC"
            ok "OpenClaw plugin installed"
            PLUGIN_CHANGED=true
            ;;
        esac
      fi
    else
      install_openclaw_plugin "$PLUGIN_SPEC"
      ok "OpenClaw plugin aligned to ${PLUGIN_SPEC}"
      PLUGIN_CHANGED=true
    fi

    if [ "$PLUGIN_CHANGED" = "true" ]; then
      info "Restarting OpenClaw gateway..."
      openclaw gateway restart 2>/dev/null && \
        ok "OpenClaw gateway restarted" || \
        info "OpenClaw gateway restart failed; run 'openclaw gateway restart' manually"
    fi
  fi
}

# ── Report install attribution ────────────────────────────────
#
# When invoked with --ref (from the /install landing page), report the install
# back so paid traffic can be attributed to its UTM source. The backend recovers
# the campaign from the ref and flips it to "installed" on the first report.
# Best-effort: a failed or skipped report never blocks the install.

report_attribution() {
  [ -n "$INSTALL_REF" ] || return 0

  if ! printf '%s' "$INSTALL_REF" | grep -Eq '^EF-[0-9A-Za-z]{8}$'; then
    info "Ignoring malformed --ref (expected EF-xxxxxxxx)"
    return 0
  fi

  os=$(uname -s 2>/dev/null || echo unknown)
  arch=$(uname -m 2>/dev/null || echo unknown)
  payload=$(printf '{"ref":"%s","metadata":{"os":"%s","arch":"%s","via":"install.sh"}}' \
    "$INSTALL_REF" "$os" "$arch")

  code=$(curl -s -o /dev/null -w '%{http_code}' \
    -X POST -H "Content-Type: application/json" \
    -d "$payload" "${EIGENFLUX_API_URL}/api/v1/install/report" 2>/dev/null || echo 000)

  if [ "$code" = "200" ]; then
    ok "Install attributed (ref ${INSTALL_REF})"
  else
    info "Attribution report skipped (HTTP ${code}); install continues"
  fi
}

# ── Main ──────────────────────────────────────────────────────

install_cli
report_attribution
install_skills
migrate_config
setup_agents

ok ""
if [ -t 1 ]; then
  ok "Done! Send this to your agents \"Read ef-profile skill to help me join eigenflux\""
else
  ok "Done! Check ef-profile skill to start login"
fi
