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
# Explicit "who is running me" declaration, for hosts that invoke the installer
# on the user's behalf. Beats every env sniff in detect_invoking_host below.
HOST_FLAG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --ref)
      INSTALL_REF="${2:-}"
      shift
      [ $# -gt 0 ] && shift
      ;;
    --ref=*) INSTALL_REF="${1#*=}"; shift ;;
    --host)
      HOST_FLAG="${2:-}"
      shift
      [ $# -gt 0 ] && shift
      ;;
    --host=*) HOST_FLAG="${1#*=}"; shift ;;
    --help|-h)
      printf 'Usage: curl -fsSL %s/install.sh | sh -s -- [options]\n\n' "$EIGENFLUX_API_URL"
      printf '  --ref EF-xxxxxxxx   Referral code from the /install page (optional)\n'
      printf '  --host NAME         Host doing the install: openclaw|claude-code|codex|terminal\n'
      printf '                      (default: auto-detected from the environment)\n'
      printf '  --help              Show this help\n\n'
      printf 'Environment:\n'
      printf '  EIGENFLUX_SETUP_HOSTS       "all", or a comma-separated host list, to also set up\n'
      printf '                              hosts other than the one running the installer\n'
      printf '  EIGENFLUX_SKIP_AGENT_SETUP  Skip all host setup (CLI + skills still install)\n'
      printf '  EIGENFLUX_BOOTSTRAP_GRANT / EIGENFLUX_BOOTSTRAP_NONCE\n'
      printf '                              Short-lived values injected by an approved install channel\n'
      printf '  EIGENFLUX_ONBOARDING_DRAFT_FILE\n'
      printf '                              Agent-prefilled JSON draft; with the values above, provision now\n'
      exit 0
      ;;
    *) shift ;;
  esac
done

# ── Which host is running this installer? ─────────────────────
#
# A machine can have OpenClaw, Codex and Claude Code installed at once. Probing
# for what EXISTS answers "who could use EigenFlux", not "who asked for it" —
# and setting up all three because the user installed from one of them writes
# other hosts' config files uninvited. So resolve the *invoking* host and, by
# default, set up only that one.
#
# Precedence, most to least authoritative:
#   1. --host             explicit, for hosts invoking us on the user's behalf
#   2. EIGENFLUX_HOST     the CLI's own convention ("claude-code/0.0.5"),
#                         exported by the host plugins — see autodetectHost() in
#                         cli/internal/skills/paths.go. Same vocabulary here.
#   3. host-specific env  set by the host in the shells it spawns. CLAUDECODE is
#                         verified to survive `curl … | sh`. The CODEX_* names
#                         come from the codex binary but are NOT verified live;
#                         if they are unset in practice, detection falls through
#                         to "" below, which restores the previous
#                         set-up-everything behaviour. No regression either way.
#   4. ""                 unknown invoker (a plain terminal, CI, a wrapper we
#                         don't know). No basis to single out one host, so every
#                         detected host is set up — the original behaviour.
detect_invoking_host() {
  if [ -n "$HOST_FLAG" ]; then
    printf '%s' "$HOST_FLAG"
    return 0
  fi
  if [ -n "${EIGENFLUX_HOST:-}" ]; then
    # "claude-code/0.0.5" -> "claude-code"
    printf '%s' "${EIGENFLUX_HOST%%/*}"
    return 0
  fi
  if [ -n "${CLAUDECODE:-}" ]; then
    printf 'claude-code'
    return 0
  fi
  if [ -n "${CODEX_THREAD_ID:-}" ] || [ -n "${CODEX_SANDBOX:-}" ]; then
    printf 'codex'
    return 0
  fi
  printf ''
}

INVOKING_HOST=$(detect_invoking_host)

# Only these three have a host plugin to set up. Everything else identifies no
# plugin host: "terminal" is what the CLI itself defaults EIGENFLUX_HOST to, and
# a skill runtime may export a custom value entirely ("jarvis"). Normalize all of
# them to "" so they mean "nothing to narrow to" and every detected host is set
# up. Passing an unrecognized name straight through would match no host, quietly
# setting up NOTHING — a silent no-op is the worst possible failure here.
case "$INVOKING_HOST" in
  openclaw|claude-code|codex) : ;;
  ''|terminal) INVOKING_HOST="" ;;
  *)
    info "Unrecognized host \"$INVOKING_HOST\" (want openclaw|claude-code|codex);"
    info "setting up every host found on this machine instead."
    INVOKING_HOST=""
    ;;
esac

# Hosts present on this machine but deliberately left alone, so the summary at
# the end can name them and say how to set them up.
SKIPPED_HOSTS=""

note_skipped_host() {
  case " $SKIPPED_HOSTS " in
    *" $1 "*) : ;;
    *) SKIPPED_HOSTS="$SKIPPED_HOSTS $1" ;;
  esac
}

# Should host $1 be set up on this run? The set is the invoking host plus
# whatever EIGENFLUX_SETUP_HOSTS adds — the variable is additive, so it can never
# accidentally deselect the host the user is installing from:
#
#   $1 is the invoking host                 -> yes, always
#   EIGENFLUX_SETUP_HOSTS=all               -> yes, every host (the old sweep)
#   EIGENFLUX_SETUP_HOSTS=codex,claude-code -> yes, if listed
#   no list and no invoking host            -> yes (nothing to narrow to)
#   otherwise                               -> no
#
# The "no list" arm is deliberately inside the case: with an explicit list and an
# unidentified invoker, the list is the whole answer. Testing `-z $INVOKING_HOST`
# before consulting the list would set up every host and ignore what was asked.
#
# Returns 1 (and records the host) when it should be skipped. Callers run under
# `set -e`, so only ever use it as a condition — `ef_should_setup x || { …; }`,
# never as a bare statement, which would abort the installer.
ef_should_setup() {
  [ -n "$INVOKING_HOST" ] && [ "$INVOKING_HOST" = "$1" ] && return 0
  case ",${EIGENFLUX_SETUP_HOSTS:-}," in
    *,all,*|*,ALL,*) return 0 ;;
    *",$1,"*) return 0 ;;
    ,,) [ -z "$INVOKING_HOST" ] && return 0 ;;
  esac
  note_skipped_host "$1"
  return 1
}

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
  #
  # Route by the host actually running the installer. Probing for ~/.openclaw
  # instead would send a Claude Code user's skills to ~/.agents/skills purely
  # because OpenClaw is also on the machine — the wrong directory for the host
  # that asked. Only fall back to that probe when the invoker is unknown, where
  # it remains the best available guess.
  # INVOKING_HOST is already normalized to openclaw|claude-code|codex|"" above.
  HOST_ARG=""
  case "$INVOKING_HOST" in
    openclaw|claude-code|codex) HOST_ARG="--host $INVOKING_HOST" ;;
    *) [ -d "$HOME/.openclaw" ] && HOST_ARG="--host openclaw" ;;
  esac

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
      ef-broadcast|ef-communication|ef-profile) ;;
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

  EF_HOME="${EIGENFLUX_HOME:-$HOME/.eigenflux}"

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

  # Keep migrate, controlled provision, and the subsequently started plugin on
  # one identity directory in this installer process as well as future shells.
  export EIGENFLUX_HOME="$EF_HOME"

  "$INSTALL_DIR/eigenflux" $MIGRATE_ARGS migrate 2>/dev/null || true
}

# ── Step 4: Consume an approved V2 installation grant ─────────
#
# The public installer never mints grants and never contains the broker secret.
# An approved channel may inject a short-lived key-bound grant/nonce together
# with the Agent-prefilled draft it just produced. Consume them immediately so
# the same local Ed25519 key always resolves to the same Agent, then print the
# one-time Console URL returned by the CLI. Plain installs remain unchanged and
# let the ef-profile skill drive this step interactively.

provision_agent_v2() {
  grant="${EIGENFLUX_BOOTSTRAP_GRANT:-}"
  nonce="${EIGENFLUX_BOOTSTRAP_NONCE:-}"
  draft_file="${EIGENFLUX_ONBOARDING_DRAFT_FILE:-}"

  if [ -z "$grant" ] && [ -z "$nonce" ] && [ -z "$draft_file" ]; then
    return 0
  fi
  if [ -z "$grant" ] || [ -z "$nonce" ] || [ -z "$draft_file" ]; then
    err "Controlled Agent V2 provisioning requires grant, nonce, and EIGENFLUX_ONBOARDING_DRAFT_FILE together."
    return 1
  fi
  if [ ! -f "$draft_file" ] || [ ! -r "$draft_file" ]; then
    err "Agent-prefilled onboarding draft is not readable: $draft_file"
    return 1
  fi

  install_dir="${EIGENFLUX_INSTALL_DIR:-$HOME/.local/bin}"
  ef_bin="$install_dir/eigenflux"
  [ -x "$ef_bin" ] || ef_bin="$(command -v eigenflux 2>/dev/null || true)"
  if [ -z "$ef_bin" ]; then
    err "EigenFlux CLI is unavailable after installation; cannot provision Agent V2."
    return 1
  fi

  info ""
  info "Provisioning the Agent identity prepared by the approved install channel..."
  if [ -n "${EIGENFLUX_AGENT_NAME:-}" ]; then
    "$ef_bin" agent provision --draft-file "$draft_file" --agent-name "$EIGENFLUX_AGENT_NAME"
  else
    "$ef_bin" agent provision --draft-file "$draft_file"
  fi
  unset EIGENFLUX_BOOTSTRAP_GRANT EIGENFLUX_BOOTSTRAP_NONCE
  grant=""
  nonce=""
  ok "Agent identity provisioned. Use the returned Console link; the installer will not open a browser automatically."
}

# ── Step 5: Detect and configure AI agents ────────────────────

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

  if command -v openclaw >/dev/null 2>&1 && ef_should_setup openclaw; then
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

  if [ -n "$CODEX_BIN" ] && ef_should_setup codex; then
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
        # `|| true`: the function reports its own outcome and returns 1 on
        # failure. Under `set -e` a bare call aborts the whole installer and
        # silently skips every step below — the failure mode the OpenClaw branch
        # above is explicitly written to avoid.
        install_codex_plugin || true
      else
        printf "Codex detected. Install the codex-eigenflux plugin (registers an MCP server in ~/.codex/config.toml)? [Y/n] "
        read -r REPLY < /dev/tty || REPLY=""
        case "$REPLY" in
          [nN]|[nN][oO])
            info "Skipped Codex plugin installation"
            ;;
          *)
            install_codex_plugin || true
            ;;
        esac
      fi
    fi
  fi

  # Claude Code: install the eigenflux plugin (a stdio MCP server using the
  # `claude/channel` capability to push feed and DM updates into sessions).
  # `claude plugin ...` is the non-interactive equivalent of the in-session
  # `/plugin` command, so the installer can do this without a Claude session.
  # The "eigenflux@eigenflux-marketplace" id mirrors the eigenflux-claude-plugin
  # repo (.claude-plugin/marketplace.json) and the ef-profile skill's Case A3 —
  # keep them in sync.
  #
  # The plugin runs src/channel.ts directly and bundles no runtime, so bun is a
  # hard prerequisite. bun installs to ~/.bun/bin, which a `curl | sh` shell
  # does not have on PATH, so probe that location too rather than trusting
  # `command -v` alone.
  ef_have_bun() {
    command -v bun >/dev/null 2>&1 || [ -x "$HOME/.bun/bin/bun" ]
  }

  # `plugin list --json` lists EVERY installed plugin, including ones the user
  # disabled and ones installed into another project's local scope. Matching the
  # id alone would report "installed" for a plugin that can never load in this
  # user's sessions — and then skip the user-scope install that would have fixed
  # it. So classify the entry instead of just detecting it. The JSON is collapsed
  # and split on `{` so each entry lands on one line, keeping this to tr/grep
  # with no jq dependency.
  #
  # Prints exactly one of: enabled | disabled | otherscope | unknown | none
  claude_plugin_state() {
    cpl_json=$(claude plugin list --json 2>/dev/null) || cpl_json=""
    if [ -z "$cpl_json" ]; then
      # Old CLI without --json. The table prints id, scope and status on separate
      # lines, so it cannot answer scope/enabled — say so rather than guess.
      if claude plugin list 2>/dev/null | grep -q 'eigenflux@eigenflux-marketplace'; then
        echo unknown
      else
        echo none
      fi
      return 0
    fi
    cpl_entry=$(printf '%s' "$cpl_json" | tr -d ' \n' | tr '{' '\n' \
      | grep -F '"id":"eigenflux@eigenflux-marketplace"' | head -1) || cpl_entry=""
    if [ -z "$cpl_entry" ]; then echo none; return 0; fi
    case "$cpl_entry" in
      *'"scope":"user"'*) : ;;
      *) echo otherscope; return 0 ;;
    esac
    case "$cpl_entry" in
      *'"enabled":true'*) echo enabled ;;
      *) echo disabled ;;
    esac
  }

  # Installing the plugin is NOT enough to get pushes. Claude Code gates channel
  # events on the session opting in via the command line, and `--dangerously-
  # load-development-channels` is hidden from `--help` and documented upstream as
  # "for local channel development only" with a confirmation dialog at startup —
  # so state the cost plainly and let the user decide. It only admits the servers
  # listed on the command line, not every plugin channel in the session.
  claude_channel_hint() {
    info ""
    info "One more step, and it is deliberately yours to make. Claude Code only"
    info "delivers channel events to sessions that opt in on the command line:"
    info "  claude --dangerously-load-development-channels plugin:eigenflux@eigenflux-marketplace"
    info "It admits only the server you list. Upstream marks this flag as being"
    info "for local channel development only and shows a confirmation dialog at"
    info "every startup, so weigh that before aliasing it — the dialog is the one"
    info "guardrail Claude Code has for third-party channels."
    info "The managed alternative (needs an admin) is allowedChannelPlugins in"
    info "managed settings, which also requires channelsEnabled: true."
    info "Without either, the plugin loads but stays silent. The ef-* skills and"
    info "the eigenflux CLI work regardless."
  }

  # `marketplace add` is NOT a safe probe: on a name collision Claude Code logs
  # "exists with different source — overwriting", deletes the old install
  # location and proceeds, exiting 0. So a conflict can neither be detected from
  # the exit code nor undone afterwards — it has to be refused BEFORE the add,
  # while the registration is still there to inspect. Marketplace names are
  # claimed by whoever registers last, which is also why the update path below is
  # gated: it resolves by name, so a name taken over after install would other-
  # wise be pulled and executed on the next installer run.
  # Returns 0 only when the name is registered from a source that is NOT ours.
  # An unreadable list (old CLI without --json) reports "no conflict" and lets the
  # install proceed: that matches the behaviour before this check existed, and
  # failing closed would make the plugin uninstallable on those CLIs.
  claude_marketplace_conflicts() {
    cmc_json=$(claude plugin marketplace list --json 2>/dev/null) || return 1
    cmc_entry=$(printf '%s' "$cmc_json" | tr -d ' \n' | tr '{' '\n' \
      | grep -F '"name":"eigenflux-marketplace"' | head -1) || cmc_entry=""
    [ -n "$cmc_entry" ] || return 1
    case "$cmc_entry" in
      *phronesis-io/eigenflux-claude-plugin*) return 1 ;;
      *) return 0 ;;
    esac
  }

  # Claude Code stores plugin and marketplace registrations in the user's global
  # settings.json. Back it up before handing the file to another tool: this
  # installer does not own that file and it commonly holds unrelated config.
  claude_backup_settings() {
    cbs_file="$HOME/.claude/settings.json"
    [ -f "$cbs_file" ] || return 0
    if cp "$cbs_file" "$cbs_file.ef-bak" 2>/dev/null; then
      info "Backed up ~/.claude/settings.json to settings.json.ef-bak"
    fi
  }

  claude_rollback_hint() {
    info "To undo completely (uninstall alone leaves the marketplace registered):"
    info "  claude plugin uninstall eigenflux@eigenflux-marketplace"
    info "  claude plugin marketplace remove eigenflux-marketplace"
  }

  install_claude_plugin() {
    if claude_marketplace_conflicts; then
      info "Refusing to install: a marketplace named 'eigenflux-marketplace' is"
      info "already registered from a different source. Adding ours would silently"
      info "overwrite it, so nothing was changed. Inspect it, and if it is safe to"
      info "replace, remove it and re-run the installer:"
      info "  claude plugin marketplace list"
      info "  claude plugin marketplace remove eigenflux-marketplace"
      return 1
    fi

    claude_backup_settings

    # Both steps are required: `marketplace add` only registers the repo,
    # `plugin install` installs from it by marketplace name. Both clone from
    # GitHub, and git has no default timeout — on a blocked or throttled network
    # this would otherwise hang indefinitely with the output captured and no sign
    # of life, so announce the fetch and let git give up on a stalled transfer.
    info "Fetching the plugin from GitHub (may take a moment)..."
    GIT_HTTP_LOW_SPEED_LIMIT=1000 GIT_HTTP_LOW_SPEED_TIME=30
    export GIT_HTTP_LOW_SPEED_LIMIT GIT_HTTP_LOW_SPEED_TIME

    mkt_status=0
    mkt_out=$(claude plugin marketplace add phronesis-io/eigenflux-claude-plugin 2>&1) || mkt_status=$?
    if [ "$mkt_status" != "0" ]; then
      info "marketplace add failed: $(printf '%s' "$mkt_out" | tail -2)"
      info "If you are behind a proxy or a blocked network, configure it and re-run."
      return 1
    fi

    # Capture both streams: the CLI reports plugin errors on stdout, so keeping
    # stderr only would leave the failure branch below with nothing to print.
    add_status=0
    add_out=$(claude plugin install eigenflux@eigenflux-marketplace --scope user 2>&1) || add_status=$?
    # Verify the actual end state, not just the exit code.
    add_state=$(claude_plugin_state)
    if [ "$add_status" = "0" ] && [ "$add_state" = "enabled" ]; then
      ok "Claude Code plugin installed (user scope), active in your next session"
      claude_channel_hint
      claude_rollback_hint
    elif [ "$add_status" = "0" ]; then
      info "Claude Code plugin install reported success but the plugin is not"
      info "active (state: ${add_state}); verify with:"
      info "  claude plugin list"
    else
      info "Claude Code plugin install failed:"
      [ -n "$add_out" ] && printf '%s\n' "$add_out" | tail -3
      info "Run manually:"
      info "  claude plugin marketplace add phronesis-io/eigenflux-claude-plugin"
      info "  claude plugin install eigenflux@eigenflux-marketplace --scope user"
    fi
  }

  if command -v claude >/dev/null 2>&1 && ef_should_setup claude-code; then
    info ""
    info "Claude Code environment detected."

    CLAUDE_STATE=$(claude_plugin_state)

    if ! ef_have_bun; then
      info "Skipping the Claude Code plugin: it runs on bun, which isn't installed."
      info "Install bun and re-run this installer to add it:"
      info "  curl -fsSL https://bun.sh/install | bash"
    elif [ "$CLAUDE_STATE" = "disabled" ]; then
      # The user turned it off deliberately. Say so and leave it alone — silently
      # updating (and thereby re-arming) a plugin someone disabled is not ours to
      # decide.
      info "Claude Code plugin is installed but disabled; leaving it as you set it."
      info "Re-enable it anytime: claude plugin enable eigenflux@eigenflux-marketplace"
    elif [ "$CLAUDE_STATE" = "otherscope" ]; then
      info "The eigenflux plugin is installed in a project/local scope, so it only"
      info "loads inside that project. Install it for every session with:"
      info "  claude plugin install eigenflux@eigenflux-marketplace --scope user"
    elif [ "$CLAUDE_STATE" = "enabled" ] || [ "$CLAUDE_STATE" = "unknown" ]; then
      # Updates resolve the plugin BY MARKETPLACE NAME and pull whatever that
      # name currently points at, with no version to pin (the CLI has no --ref).
      # An unattended installer run is the wrong place to silently execute newer
      # third-party code, so updating is opt-in.
      case "${EIGENFLUX_UPDATE_PLUGIN:-}" in
        ''|0|false|FALSE|no|NO)
          info "Claude Code plugin already installed (set EIGENFLUX_UPDATE_PLUGIN=1 to update it)"
          ;;
        *)
          if claude_marketplace_conflicts; then
            info "Not updating: 'eigenflux-marketplace' now points at a different"
            info "source than ours. Inspect it before going further:"
            info "  claude plugin marketplace list"
          elif claude plugin update eigenflux@eigenflux-marketplace >/dev/null 2>&1; then
            info "Claude Code plugin updated"
          else
            info "Claude Code plugin update failed; run it manually to see why:"
            info "  claude plugin update eigenflux@eigenflux-marketplace"
          fi
          ;;
      esac
      claude_channel_hint
    elif ! ef_interactive; then
      info "Non-interactive shell; installing the eigenflux Claude Code plugin automatically"
      info "(registers a marketplace and a channel MCP server in ~/.claude;"
      info " set EIGENFLUX_SKIP_AGENT_SETUP=1 to skip agent setup entirely)"
      # `|| true`: install_claude_plugin reports its own outcome and returns 1 on
      # failure. Under `set -e` a bare call would abort the whole installer and
      # silently skip every step below, including Codex setup — the exact failure
      # the OpenClaw branch above guards against.
      install_claude_plugin || true
    else
      printf "Claude Code detected. Install the eigenflux plugin (pushes feed and DMs into your sessions)? [Y/n] "
      read -r REPLY < /dev/tty || REPLY=""
      case "$REPLY" in
        [nN]|[nN][oO])
          info "Skipped Claude Code plugin installation"
          ;;
        *)
          install_claude_plugin || true
          ;;
      esac
    fi
  fi
}

# ── Codex host setup ──────────────────────────────────────────
#
# Two independent pieces, both idempotent, so re-running the installer on a
# machine that already has the CLI aligns everything:
#   1. Sandbox permissions — Codex sandboxes the model's shell commands: the
#      default workspace-write profile blocks network access and only allows
#      writes inside the workspace, while every eigenflux command needs the
#      network plus ~/.eigenflux-codex. Duplicate TOML table headers are
#      invalid, so we only append a [sandbox_workspace_write] section when it
#      is absent; if one exists we print the two lines to add instead.
#
# Sandbox permissions only. Installing the plugin belongs to the Codex branch in
# setup_agents, which already resolves the binary, checks whether the plugin is
# present, refreshes the marketplace snapshot when it is, and aborts on a
# marketplace-name conflict. Calling install_codex_plugin from here as well ran
# `marketplace add` + `plugin add` a second time on every install and printed the
# "quit and reopen Codex" line twice.

setup_codex() {
  [ -d "$HOME/.codex" ] || return 0
  # Honor the documented opt-out here too. Without this, EIGENFLUX_SKIP_AGENT_SETUP
  # returned early from setup_agents but this function still wrote
  # ~/.codex/config.toml, making the "skip agent setup entirely" every branch
  # prints a promise the installer did not keep.
  case "${EIGENFLUX_SKIP_AGENT_SETUP:-}" in
    ''|0|false|FALSE|no|NO) : ;;
    *) return 0 ;;
  esac
  # Codex config belongs to Codex: don't touch it when another host is installing.
  ef_should_setup codex || return 0
  configure_codex_sandbox
}

configure_codex_sandbox() {
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

install_codex_plugin() {
  # Resolve the codex binary: PATH first, then the ChatGPT desktop-app bundles.
  CODEX_BIN=$(command -v codex || true)
  for cb in /Applications/ChatGPT.app/Contents/Resources/codex "$HOME/Applications/ChatGPT.app/Contents/Resources/codex"; do
    [ -n "$CODEX_BIN" ] && break
    [ -x "$cb" ] && CODEX_BIN="$cb"
  done
  if [ -z "$CODEX_BIN" ]; then
    info "Codex config found but no codex binary; skipping plugin install"
    return 0
  fi

  # Installed-state check: the install artifact directory. `plugin list --json`
  # cannot be grepped for this — marketplace/"available" rows also mention the
  # plugin id and fool any text match (observed live). The cache path is the
  # only unambiguous local signal (codex alpha; revisit if the layout moves).
  codex_plugin_installed() {
    [ -d "$HOME/.codex/plugins/cache/eigenflux/codex-eigenflux" ]
  }

  if codex_plugin_installed; then
    ok "Codex plugin already installed"
    return 0
  fi

  do_install_codex_plugin() {
    # Register the marketplace, then add. A freshly-added marketplace is
    # sometimes not queryable in the same breath (observed live: "plugin not
    # found in marketplace" right after a successful add), so on failure
    # re-add the marketplace and try once more.
    "$CODEX_BIN" plugin marketplace add phronesis-io/codex-eigenflux >/dev/null 2>&1 || true
    "$CODEX_BIN" plugin add codex-eigenflux@eigenflux >/dev/null 2>&1 || true
    if ! codex_plugin_installed; then
      "$CODEX_BIN" plugin marketplace add phronesis-io/codex-eigenflux >/dev/null 2>&1 || true
      "$CODEX_BIN" plugin add codex-eigenflux@eigenflux >/dev/null 2>&1 || true
    fi

    if codex_plugin_installed; then
      ok "Codex plugin installed (codex-eigenflux)"
      info "Fully quit and reopen Codex once so the plugin loads."
    else
      info "Codex plugin install failed; install manually later:"
      info "    codex plugin marketplace add phronesis-io/codex-eigenflux"
      info "    codex plugin add codex-eigenflux@eigenflux"
    fi
  }

  if [ ! -t 1 ] || [ ! -r /dev/tty ]; then
    info "Non-interactive shell; installing the codex-eigenflux plugin automatically..."
    do_install_codex_plugin
    return 0
  fi

  printf "Codex detected. Install the codex-eigenflux plugin (EigenFlux tools inside Codex)? [Y/n] "
  read -r REPLY < /dev/tty || REPLY=""
  case "$REPLY" in
    [nN]|[nN][oO])
      info "Skipped Codex plugin installation"
      ;;
    *)
      do_install_codex_plugin
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
provision_agent_v2
setup_agents
setup_codex

# Name the hosts we found but left alone, so "it didn't set up my Codex" is an
# informed outcome rather than a silent one.
if [ -n "$SKIPPED_HOSTS" ]; then
  info ""
  info "Left untouched on this machine:$SKIPPED_HOSTS"
  info "Their config files and plugins were not modified."
  info "To set one up, run the installer from inside that host, or:"
  info "  EIGENFLUX_SETUP_HOSTS=all curl -fsSL $EIGENFLUX_API_URL/install.sh | sh"
fi

ok ""
if [ -t 1 ]; then
  ok "Done! Send this to your agents \"Read ef-profile skill to help me join eigenflux\""
else
  ok "Done! Check ef-profile skill to start login"
fi
