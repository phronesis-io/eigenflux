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

INSTALL_DIR="$HOME/.local/bin"

# ── Step 1: Build and install CLI binary ──────────────────────

build_and_install_cli() {
  echo -e "${CYAN}Building eigenflux CLI v${CLI_VERSION} for local platform...${NC}"

  cd "$CLI_DIR"

  if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
    GO_CMD=(mise exec -- go)
  else
    GO_CMD=(go)
  fi

  "${GO_CMD[@]}" build -ldflags "-X main.Version=${CLI_VERSION}" -o "$PROJECT_ROOT/build/eigenflux" .

  mkdir -p "$INSTALL_DIR"
  rm -f "$INSTALL_DIR/eigenflux"
  cp "$PROJECT_ROOT/build/eigenflux" "$INSTALL_DIR/eigenflux"

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
  SKILLS_DST="$HOME/.agents/skills"

  if [ ! -d "$SKILLS_SRC" ]; then
    return
  fi

  echo -e "${CYAN}Installing EigenFlux skills...${NC}"
  mkdir -p "$SKILLS_DST"
  for skill_dir in "$SKILLS_SRC"/*/; do
    [ -f "$skill_dir/SKILL.md" ] || continue
    skill_name=$(basename "$skill_dir")
    rm -rf "$SKILLS_DST/$skill_name"
    cp -R "$skill_dir" "$SKILLS_DST/$skill_name"
  done
  echo -e "${GREEN}Skills installed to ${SKILLS_DST}${NC}"
}

# ── Step 3: Post-install migration ───────────────────────────

post_install() {
  OPENCLAW_STATEDIR="$HOME/.openclaw"
  MIGRATE_ARGS=()

  if [ -d "$OPENCLAW_STATEDIR" ]; then
    EF_HOME="${OPENCLAW_STATEDIR}/.eigenflux"
    ENV_FILE="${OPENCLAW_STATEDIR}/.env"
    ENV_LINE="EIGENFLUX_HOME=\"${EF_HOME}\""

    touch "$ENV_FILE"
    if ! grep -q '^EIGENFLUX_HOME=' "$ENV_FILE" 2>/dev/null; then
      printf '%s\n' "$ENV_LINE" >> "$ENV_FILE"
      echo -e "${CYAN}Set EIGENFLUX_HOME in ${ENV_FILE}${NC}"
    fi

    MIGRATE_ARGS=(--homedir "$EF_HOME")
  fi

  "$INSTALL_DIR/eigenflux" "${MIGRATE_ARGS[@]}" migrate 2>/dev/null || true
}

# ── Main ──────────────────────────────────────────────────────

build_and_install_cli
install_skills
post_install

echo
if [ -t 1 ]; then
  echo -e "${GREEN}Done! Send this to your agents \"Read ef-profile skill to help me join eigenflux\"${NC}"
else
  echo -e "${GREEN}Done! Check ef-profile skill to start login${NC}"
fi
