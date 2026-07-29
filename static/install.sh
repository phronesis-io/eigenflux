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
  info ""
  info "Installing EigenFlux skills..."

  EF_BIN="${EIGENFLUX_INSTALL_DIR:-$HOME/.local/bin}/eigenflux"
  [ -x "$EF_BIN" ] || EF_BIN="$(command -v eigenflux 2>/dev/null || true)"

  # R2 is the authoritative skills source for a released CLI. Pass --host
  # explicitly: at install time the host statedir/env may not be ready, so
  # autodetect could misroute. (gate-4: openclaw/codex/terminal all load
  # ~/.agents/skills; only claude-code uses ~/.claude/skills.)
  HOST_ARG=""
  [ -d "$HOME/.openclaw" ] && HOST_ARG="--host openclaw"

  if [ -n "$EF_BIN" ] && "$EF_BIN" skills sync $HOST_ARG >/dev/null 2>&1; then
    ok "EigenFlux skills synced from R2"
    return
  fi

  info "R2 unreachable — bootstrapping skills from GitHub (provisional, replaced on next sync)"

  # Fallback ONLY when R2 is down. The bootstrap copy is marked provisional
  # (.ef-stale) and has no cli_version manifest, so the next `skills sync`
  # bypasses its --if-stale short-circuit and force-replaces it from R2.
  # Resolve the host's real load dir via the CLI (offline path resolution) so a
  # claude-code host gets ~/.claude/skills, not a hardcoded ~/.agents/skills.
  SKILLS_DIR=""
  [ -n "$EF_BIN" ] && SKILLS_DIR="$("$EF_BIN" skills path $HOST_ARG 2>/dev/null || true)"
  [ -n "$SKILLS_DIR" ] || SKILLS_DIR="$HOME/.agents/skills"
  TMP_DIR=$(mktemp -d)
  trap "rm -rf '$TMP_DIR'" EXIT

  TARBALL_URL="https://github.com/${GITHUB_REPO}/archive/refs/heads/${BRANCH}.tar.gz"
  if ! curl -fsSL "$TARBALL_URL" | tar xz -C "$TMP_DIR" 2>/dev/null; then
    info "Skills installation skipped (no R2, GitHub download failed)"
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
    # Only the production allowlist — never ship dev-only skills (e.g. ef-localdev).
    case "$skill_name" in
      ef-broadcast|ef-communication|ef-profile|ef-trading) ;;
      *) continue ;;
    esac
    rm -rf "$SKILLS_DIR/$skill_name"
    cp -R "$skill_dir" "$SKILLS_DIR/$skill_name"
  done
  : > "$SKILLS_DIR/.ef-stale"

  ok "EigenFlux skills bootstrapped to ${SKILLS_DIR} (provisional — will refresh from R2 on next sync)"
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
  # Opt-out: skip all agent/plugin auto-setup (CLI + skills still install).
  # Only a truthy value skips; SKIP=0/false/no means "do NOT skip".
  case "${EIGENFLUX_SKIP_AGENT_SETUP:-}" in
    ''|0|false|FALSE|no|NO) : ;;
    *) info "EIGENFLUX_SKIP_AGENT_SETUP set; skipping agent plugin setup"; return 0 ;;
  esac

  # Interactive iff we can actually open the controlling terminal. stdout may
  # be piped (`... | tee log`) while the user is still there to answer, so
  # don't gate on `-t 1`; and `-r /dev/tty` only checks permission bits, so
  # open it for real. Used by both the OpenClaw and Codex branches so they
  # never disagree about whether to prompt.
  ef_interactive() { ( : < /dev/tty ) 2>/dev/null; }

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
      # Each call is wrapped in `if` so a plugin failure never aborts the
      # whole installer under `set -e` (it would silently skip every branch
      # below, e.g. Codex setup on a dual-host machine).
      if ! ef_interactive; then
        info "Non-interactive shell; installing the openclaw-eigenflux plugin automatically"
        info "(installs into OpenClaw's plugin dir and restarts the gateway;"
        info " set EIGENFLUX_SKIP_AGENT_SETUP=1 to skip agent setup entirely)"
        if install_openclaw_plugin "$PLUGIN_SPEC"; then
          ok "OpenClaw plugin installed"
          PLUGIN_CHANGED=true
        else
          info "OpenClaw plugin install failed; run manually: openclaw plugins install ${PLUGIN_SPEC}"
        fi
      else
        printf "OpenClaw detected. Install the openclaw-eigenflux plugin automatically? [Y/n] "
        read -r REPLY < /dev/tty || REPLY=""
        case "$REPLY" in
          [nN]|[nN][oO])
            info "Skipped OpenClaw plugin installation"
            ;;
          *)
            info "Installing ${PLUGIN_SPEC}..."
            if install_openclaw_plugin "$PLUGIN_SPEC"; then
              ok "OpenClaw plugin installed"
              PLUGIN_CHANGED=true
            else
              info "OpenClaw plugin install failed; run manually: openclaw plugins install ${PLUGIN_SPEC}"
            fi
            ;;
        esac
      fi
    else
      if install_openclaw_plugin "$PLUGIN_SPEC"; then
        ok "OpenClaw plugin aligned to ${PLUGIN_SPEC}"
        PLUGIN_CHANGED=true
      else
        info "OpenClaw plugin update failed; run manually: openclaw plugins install ${PLUGIN_SPEC}"
      fi
    fi

    if [ "$PLUGIN_CHANGED" = "true" ]; then
      info "Restarting OpenClaw gateway..."
      openclaw gateway restart 2>/dev/null && \
        ok "OpenClaw gateway restarted" || \
        info "OpenClaw gateway restart failed; run 'openclaw gateway restart' manually"
      info "Uninstall anytime: openclaw plugins uninstall openclaw-eigenflux"
    fi
  fi

  # Codex: install the codex-eigenflux plugin (bundled stdio MCP server that
  # exposes the feed/messages as tools and guarantees skills sync on startup).
  # ChatGPT desktop app users often have no `codex` on PATH — on macOS the CLI
  # ships inside the app bundle (/Applications or ~/Applications), so fall
  # back to those paths. Linux/WSL: codex only ships via PATH installs
  # (npm/brew), no bundle fallback needed.
  # Install commands / app paths / the "codex-eigenflux@eigenflux" id mirror
  # the codex-eigenflux repo (README, .agents/plugins/marketplace.json) and
  # the ef-profile skill's Case A2 — keep them in sync.
  CODEX_BIN=""
  if command -v codex >/dev/null 2>&1; then
    CODEX_BIN="codex"
  elif [ -x "/Applications/ChatGPT.app/Contents/Resources/codex" ]; then
    CODEX_BIN="/Applications/ChatGPT.app/Contents/Resources/codex"
  elif [ -x "$HOME/Applications/ChatGPT.app/Contents/Resources/codex" ]; then
    CODEX_BIN="$HOME/Applications/ChatGPT.app/Contents/Resources/codex"
  fi

  # Is the plugin actually installed? Prefer machine-readable output: the
  # default `plugin list --json` contains ONLY installed plugins, so a hit is
  # unambiguous. The table fallback (old CLIs) must exclude marketplace rows
  # and "not installed" entries — plain grep is famously fooled by them.
  codex_plugin_installed() {
    if "$CODEX_BIN" plugin list --json >/dev/null 2>&1; then
      "$CODEX_BIN" plugin list --json 2>/dev/null | grep -q '"codex-eigenflux@'
    else
      "$CODEX_BIN" plugin list 2>/dev/null | grep -E '^codex-eigenflux@' | grep -iqv "not installed"
    fi
  }

  install_codex_plugin() {
    # Both steps are required: `marketplace add` only registers the repo,
    # `plugin add` installs from it BY MARKETPLACE NAME. `marketplace add` is
    # idempotent for the SAME source (exit 0, even when already added). ANY
    # non-zero exit must abort — do NOT fall through to `plugin add`:
    #   - a name `eigenflux` already taken by a DIFFERENT repo (squatting)
    #     reports "already added from a different source" and non-zero; installing
    #     by that name would then pull foreign code.
    #   - network/auth failures are non-zero too and shouldn't be masked.
    # Aborting on every non-zero is the safe default; the message match below
    # only refines the hint, never the decision.
    mkt_status=0
    mkt_out=$("$CODEX_BIN" plugin marketplace add phronesis-io/codex-eigenflux 2>&1) || mkt_status=$?
    if [ "$mkt_status" != "0" ]; then
      case "$mkt_out" in
        *different\ source*|*already\ added\ from\ a\ different*)
          info "Refusing to install: a marketplace named 'eigenflux' already points at a different source." ;;
        *)
          info "marketplace add failed: $(printf '%s' "$mkt_out" | tail -2)" ;;
      esac
      info "Inspect it and, if safe, remove it, then re-run the installer:"
      info "  $CODEX_BIN plugin marketplace list"
      info "  $CODEX_BIN plugin marketplace remove eigenflux"
      return 1
    fi
    add_status=0
    add_err=$("$CODEX_BIN" plugin add codex-eigenflux@eigenflux 2>&1 >/dev/null) || add_status=$?
    # Verify the actual end state, not just the exit code.
    if [ "$add_status" = "0" ] && codex_plugin_installed; then
      ok "Codex plugin installed (registers an MCP server in ~/.codex/config.toml). Quit and reopen the Codex / ChatGPT desktop app once for it to take effect, then start a new task."
      info "Uninstall anytime: $CODEX_BIN plugin remove codex-eigenflux@eigenflux"
    elif [ "$add_status" = "0" ]; then
      # add exited 0 but the plugin isn't listed — report that, not a bare "failed".
      info "Codex plugin add reported success but the plugin isn't listed; verify with:"
      info "  $CODEX_BIN plugin list"
    else
      info "Codex plugin install failed:"
      [ -n "$add_err" ] && printf '%s\n' "$add_err" | tail -3
      info "Run manually:"
      info "  $CODEX_BIN plugin marketplace add phronesis-io/codex-eigenflux"
      info "  $CODEX_BIN plugin add codex-eigenflux@eigenflux"
    fi
  }

  if [ -n "$CODEX_BIN" ]; then
    info ""
    info "Codex environment detected."

    if codex_plugin_installed; then
      # Refresh the git marketplace snapshot so future installs/updates pick
      # up the latest plugin; codex has no direct plugin-update command yet.
      if "$CODEX_BIN" plugin marketplace upgrade eigenflux >/dev/null 2>&1; then
        info "Codex plugin already installed; refreshed marketplace snapshot"
      else
        info "Codex plugin already installed (snapshot refresh skipped)"
      fi
    else
      if ! ef_interactive; then
        info "Non-interactive shell; installing the codex-eigenflux plugin automatically"
        info "(writes ~/.codex/config.toml and registers an MCP server for future Codex sessions;"
        info " set EIGENFLUX_SKIP_AGENT_SETUP=1 to skip agent setup entirely)"
        install_codex_plugin
      else
        printf "Codex detected. Install the codex-eigenflux plugin (registers an MCP server in ~/.codex/config.toml)? [Y/n] "
        read -r REPLY < /dev/tty || REPLY=""
        case "$REPLY" in
          [nN]|[nN][oO])
            info "Skipped Codex plugin installation"
            ;;
          *)
            install_codex_plugin
            ;;
        esac
      fi
    fi
  fi
}

# ── Codex sandbox permissions ─────────────────────────────────
#
# Codex sandboxes the model's shell commands: the default workspace-write
# profile blocks network access and only allows writes inside the workspace,
# while every eigenflux command needs the network plus ~/.eigenflux-codex.
# Getting this configured at install time means users never hit a denied
# command or a surprise approval prompt later. Duplicate TOML table headers
# are invalid, so we only append a [sandbox_workspace_write] section when it
# is absent; if one already exists we print the two lines to add instead of
# editing inside it.

setup_codex() {
  [ -d "$HOME/.codex" ] || return 0

  CODEX_CFG="$HOME/.codex/config.toml"

  if [ -f "$CODEX_CFG" ]; then
    if grep -Eq '^[[:space:]]*sandbox_mode[[:space:]]*=[[:space:]]*"danger-full-access"' "$CODEX_CFG" || \
       grep -Eq '^[[:space:]]*network_access[[:space:]]*=[[:space:]]*true' "$CODEX_CFG"; then
      ok "Codex sandbox already allows network access"
      return 0
    fi
  fi

  if [ -f "$CODEX_CFG" ] && grep -Eq '^[[:space:]]*\[sandbox_workspace_write\]' "$CODEX_CFG"; then
    info "Codex detected. To let EigenFlux run without approval prompts, add these"
    info "two lines under [sandbox_workspace_write] in $CODEX_CFG:"
    info "    network_access = true"
    info "    writable_roots = [\"$HOME/.eigenflux-codex\"]"
    return 0
  fi

  CODEX_BLOCK="
# EigenFlux: let sandboxed sessions reach the network and write the eigenflux
# identity home (~/.eigenflux-codex). Added by install.sh — remove anytime.
[sandbox_workspace_write]
network_access = true
writable_roots = [\"$HOME/.eigenflux-codex\"]
"

  if [ ! -t 1 ] || [ ! -r /dev/tty ]; then
    info "Non-interactive shell; leaving Codex config untouched."
    info "For approval-free EigenFlux in Codex, add to $CODEX_CFG:"
    info "    [sandbox_workspace_write]"
    info "    network_access = true"
    info "    writable_roots = [\"$HOME/.eigenflux-codex\"]"
    return 0
  fi

  printf "Codex detected. EigenFlux needs sandbox network access and write access to ~/.eigenflux-codex — add this to %s? [Y/n] " "$CODEX_CFG"
  read -r REPLY < /dev/tty || REPLY=""
  case "$REPLY" in
    [nN]|[nN][oO])
      info "Skipped. Codex will show an approval prompt when eigenflux commands run."
      ;;
    *)
      printf '%s' "$CODEX_BLOCK" >> "$CODEX_CFG"
      ok "Codex sandbox configured for EigenFlux ($CODEX_CFG)"
      ;;
  esac
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
setup_codex

ok ""
if [ -t 1 ]; then
  ok "Done! Send this to your agents \"Read ef-profile skill to help me join eigenflux\""
else
  ok "Done! Check ef-profile skill to start login"
fi
