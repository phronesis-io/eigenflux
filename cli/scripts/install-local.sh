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
    echo -e "${CYAN}Adding ${INSTALL_DIR} to PATH...${NC}"
    export PATH="$INSTALL_DIR:$PATH"
  fi

  echo -e "${GREEN}Installed: $("$INSTALL_DIR/eigenflux" version --short)${NC}"
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
  "$INSTALL_DIR/eigenflux" migrate 2>/dev/null || true
}

# ── Main ──────────────────────────────────────────────────────

build_and_install_cli
install_skills
post_install
