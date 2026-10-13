#!/bin/bash
set -e

# ============================================================
# install-local.sh - Build and install eigenflux CLI locally
# Usage: ./cli/scripts/install-local.sh [--watch-launch-agent /absolute/path.plist]
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$CLI_DIR/.."; pwd)"
INSTALL_OS="$(uname -s)"
WATCH_LAUNCH_AGENT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --watch-launch-agent)
      if [[ $# -lt 2 || -z "$2" || "$2" == --* || -n "$WATCH_LAUNCH_AGENT" ]]; then
        echo "--watch-launch-agent requires exactly one existing absolute plist path." >&2
        exit 2
      fi
      WATCH_LAUNCH_AGENT="$2"
      shift 2
      ;;
    *)
      echo "Usage: $0 [--watch-launch-agent /absolute/path.plist]" >&2
      exit 2
      ;;
  esac
done

if [[ -n "$WATCH_LAUNCH_AGENT" ]]; then
  if [[ "$INSTALL_OS" != Darwin || "$WATCH_LAUNCH_AGENT" != /* || "$WATCH_LAUNCH_AGENT" != *.plist || ! -f "$WATCH_LAUNCH_AGENT" || ! -r "$WATCH_LAUNCH_AGENT" || -L "$WATCH_LAUNCH_AGENT" || "$(dirname "$WATCH_LAUNCH_AGENT")" != "$HOME/Library/LaunchAgents" ]]; then
    echo "--watch-launch-agent requires a readable regular .plist in this macOS user's ~/Library/LaunchAgents directory." >&2
    exit 2
  fi
fi

source "$CLI_DIR/.cli.config"
[[ -f "$CLI_DIR/.cli.env" ]] && source "$CLI_DIR/.cli.env"

if [[ -z "${EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY:-}" ]]; then
  echo "EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY is required to build a CLI that can verify Skills releases" >&2
  exit 1
fi

CLI_COMMIT=$(git -C "$PROJECT_ROOT" rev-parse --short=8 HEAD 2>/dev/null || echo "unknown")

GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

INSTALL_DIR="$HOME/.local/bin"
BUILD_STAGE=""
INSTALL_STAGE=""
HELPER_REPLACED=0
INSTALL_COMMITTED=0

cleanup_install() {
  # Restore the previous complete bundle if publication stopped before the CLI
  # was replaced. Never merge an old and a new application directory.
  if [[ -n "$INSTALL_STAGE" && "$INSTALL_COMMITTED" != 1 ]]; then
    if [[ -d "$INSTALL_STAGE/previous.app" ]]; then
      rm -rf "$INSTALL_DIR/EigenFlux Notifications.app"
      mv "$INSTALL_STAGE/previous.app" "$INSTALL_DIR/EigenFlux Notifications.app" || return
    elif [[ "$HELPER_REPLACED" == 1 ]]; then
      rm -rf "$INSTALL_DIR/EigenFlux Notifications.app"
    fi
  fi
  [[ -z "$BUILD_STAGE" ]] || rm -rf "$BUILD_STAGE"
  [[ -z "$INSTALL_STAGE" ]] || rm -rf "$INSTALL_STAGE"
}
trap cleanup_install EXIT
trap 'exit 130' HUP INT TERM

# ── Step 1: Build and install CLI binary ──────────────────────

build_and_install_cli() {
  echo -e "${CYAN}Building eigenflux CLI v${CLI_VERSION} for local platform...${NC}"

  cd "$CLI_DIR"

  if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
    GO_CMD=(mise exec -- go)
  else
    GO_CMD=(go)
  fi

  mkdir -p "$PROJECT_ROOT/build"
  BUILD_STAGE="$(mktemp -d "$PROJECT_ROOT/build/local-install.XXXXXX")"
  "${GO_CMD[@]}" build -ldflags "-X main.Version=${CLI_VERSION} -X main.Commit=${CLI_COMMIT} -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64=${EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY}" -o "$BUILD_STAGE/eigenflux" .

  if [[ "$INSTALL_OS" == Darwin ]]; then
    local arch
    arch="$("${GO_CMD[@]}" env GOARCH)"
    if ! bash "$SCRIPT_DIR/build-notifier-macos.sh" "$BUILD_STAGE" "$arch"; then
      echo "macOS notification helper build failed; the installed CLI and helper were not replaced. Install Xcode Command Line Tools, then rerun this script." >&2
      return 1
    fi
  fi

  mkdir -p "$INSTALL_DIR"
  if [[ -d "$INSTALL_DIR/eigenflux" || ( "$INSTALL_OS" == Darwin && -L "$INSTALL_DIR/EigenFlux Notifications.app" ) ]]; then
    echo "Refusing to replace an unexpected installation path; inspect $INSTALL_DIR before retrying." >&2
    return 1
  fi
  INSTALL_STAGE="$(mktemp -d "$INSTALL_DIR/.eigenflux-install.XXXXXX")"
  cp "$BUILD_STAGE/eigenflux" "$INSTALL_STAGE/eigenflux"
  if [[ "$INSTALL_OS" == Darwin ]]; then
    cp -R "$BUILD_STAGE/EigenFlux Notifications.app" "$INSTALL_STAGE/EigenFlux Notifications.app"
    if [[ -e "$INSTALL_DIR/EigenFlux Notifications.app" ]]; then
      if [[ ! -d "$INSTALL_DIR/EigenFlux Notifications.app" ]]; then
        echo "Refusing to replace a non-directory notification helper." >&2
        return 1
      fi
      mv "$INSTALL_DIR/EigenFlux Notifications.app" "$INSTALL_STAGE/previous.app"
    fi
    HELPER_REPLACED=1
    mv "$INSTALL_STAGE/EigenFlux Notifications.app" "$INSTALL_DIR/EigenFlux Notifications.app"
  fi
  mv -f "$INSTALL_STAGE/eigenflux" "$INSTALL_DIR/eigenflux"
  INSTALL_COMMITTED=1

  if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    persist_path "$INSTALL_DIR"
  fi

  echo -e "${GREEN}Installed: $("$INSTALL_DIR/eigenflux" version --short)${NC}"
}

persist_path() {
  local target_dir="$1"
  local marker="# added by eigenflux installer"
  local updated_rc=""

  _append_posix() {
    local rc="$1" create="$2"
    [[ -f "$rc" || "$create" == "create" ]] || return 0
    if [[ -f "$rc" ]] && grep -qF "$marker" "$rc" 2>/dev/null; then
      return 0
    fi
    {
      printf '\n%s\n' "$marker"
      printf 'export PATH="$HOME/.local/bin:$PATH"\n'
    } >> "$rc"
    echo -e "${CYAN}Added ${target_dir} to PATH in ${rc}${NC}"
    updated_rc="$rc"
  }

  _append_fish() {
    local rc="$HOME/.config/fish/config.fish"
    [[ -f "$rc" ]] || return 0
    if grep -qF "$marker" "$rc" 2>/dev/null; then
      return 0
    fi
    {
      printf '\n%s\n' "$marker"
      printf 'fish_add_path -g %s\n' "$target_dir"
    } >> "$rc"
    echo -e "${CYAN}Added ${target_dir} to PATH in ${rc}${NC}"
    updated_rc="$rc"
  }

  local shell_name
  shell_name=$(basename "${SHELL:-}")
  case "$shell_name" in
    zsh)  _append_posix "$HOME/.zshrc" create ;;
    bash)
      if [[ "$(uname -s)" == "Darwin" ]]; then
        _append_posix "$HOME/.bash_profile" create
      else
        _append_posix "$HOME/.bashrc" create
      fi
      ;;
    fish) _append_fish ;;
    *)
      [[ -f "$HOME/.zshrc" ]]        && _append_posix "$HOME/.zshrc"
      [[ -f "$HOME/.bashrc" ]]       && _append_posix "$HOME/.bashrc"
      [[ -f "$HOME/.bash_profile" ]] && _append_posix "$HOME/.bash_profile"
      _append_fish
      ;;
  esac

  export PATH="$target_dir:$PATH"

  if [[ -n "$updated_rc" ]]; then
    echo -e "${CYAN}Open a new terminal or run: source ${updated_rc}${NC}"
  fi
}

# ── Step 2: Install skills from local repo ────────────────────

install_skills() {
  SKILLS_SRC="$PROJECT_ROOT/skills"

  if [ ! -d "$SKILLS_SRC" ]; then
    return
  fi

  echo -e "${CYAN}Installing EigenFlux skills (from local bundle)...${NC}"
  # Reuse the CLI's atomic install path. The current runtime environment picks
  # its own skill-load directory; merely having another host installed must not
  # redirect this test install.
  "$INSTALL_DIR/eigenflux" skills install --from-bundle "$SKILLS_SRC"
}

# ── Step 3: Post-install migration ───────────────────────────

post_install() {
  # Migrate only the home inherited by the current Agent runtime. A sibling
  # ~/.openclaw directory is not evidence that OpenClaw is the caller.
  "$INSTALL_DIR/eigenflux" migrate 2>/dev/null || true
}

setup_notifications() {
  [[ "$INSTALL_OS" == Darwin ]] || return 0
  echo -e "${CYAN}Setting up macOS notifications; allow EigenFlux if macOS asks.${NC}"
  if ! "$INSTALL_DIR/eigenflux" notify setup; then
    echo "CLI and Skills are installed, but notification setup is incomplete." >&2
    echo "If permission was denied, enable EigenFlux in System Settings > Notifications." >&2
    printf 'Retry: %q notify setup\nCheck: %q notify status\n' "$INSTALL_DIR/eigenflux" "$INSTALL_DIR/eigenflux" >&2
    return 1
  fi
}

# ── Main ──────────────────────────────────────────────────────

build_and_install_cli
install_skills
post_install
POST_INSTALL_FAILED=0
if ! setup_notifications; then
  POST_INSTALL_FAILED=1
fi
if [[ -n "$WATCH_LAUNCH_AGENT" ]]; then
  echo "Restarting the specified watch LaunchAgent with its existing configuration."
  if ! "$INSTALL_DIR/eigenflux" watch restart --launch-agent "$WATCH_LAUNCH_AGENT"; then
    echo "CLI and Skills are installed, but watch restart failed; inspect the error above." >&2
    printf 'Retry: %q watch restart --launch-agent %q\n' "$INSTALL_DIR/eigenflux" "$WATCH_LAUNCH_AGENT" >&2
    POST_INSTALL_FAILED=1
  fi
else
  echo "Restart any existing watch using its original Agent Home, server and arguments to load this build."
fi
if [[ "$POST_INSTALL_FAILED" == 1 ]]; then
  exit 1
fi

echo
if [ -t 1 ]; then
  echo -e "${GREEN}Done! Send this to your agents \"Read ef-onboarding skill to help me join EigenFlux\"${NC}"
else
  echo -e "${GREEN}Done! Check ef-onboarding skill to start Console V2 onboarding${NC}"
fi
