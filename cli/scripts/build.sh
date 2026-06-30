#!/bin/bash
set -e

# ============================================================
# build.sh - Cross-compile eigenflux CLI for all platforms
# Usage: ./cli/scripts/build.sh
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$CLI_DIR/.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build/cli"

source "$CLI_DIR/.cli.config"

CLI_COMMIT=$(git -C "$PROJECT_ROOT" rev-parse --short=8 HEAD 2>/dev/null || echo "unknown")

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

# Prefer project-pinned Go via mise when available.
if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

echo -e "${CYAN}Building eigenflux CLI v${CLI_VERSION} (commit ${CLI_COMMIT})${NC}"
echo ""

failed=0
cd "$CLI_DIR"

for platform in "${PLATFORMS[@]}"; do
  IFS='/' read -r os arch <<< "$platform"
  bin_name="eigenflux-${os}-${arch}"
  if [[ "$os" == "windows" ]]; then
    bin_name="${bin_name}.exe"
  fi

  echo -ne "${CYAN}Compiling ${os}/${arch} ...${NC} "
  if GOOS="$os" GOARCH="$arch" "${GO_CMD[@]}" build \
    -ldflags "-X main.Version=${CLI_VERSION} -X main.Commit=${CLI_COMMIT}" \
    -o "$BUILD_DIR/$bin_name" . 2>&1; then
    echo -e "${GREEN}OK${NC}"
  else
    echo -e "${RED}FAILED${NC}"
    failed=1
  fi
done

# Write version file for install.sh
echo "$CLI_VERSION" > "$BUILD_DIR/version.txt"

# ── Skills bundle (shipped on R2 alongside the binaries; `eigenflux skills sync`
#    pulls it into each host's skill-load dir, so updating a skill is a CLI
#    release, not a plugin republish). ───────────────────────────────────────
SKILLS_SRC="$PROJECT_ROOT/skills"
SKILLS_STAGE="$BUILD_DIR/skills-stage"
# Single source of truth: derive the allowlist from the Go constant ProdAllowlist
# so build.sh can never drift from manifestgen/the client.
SKILLS_ALLOWLIST=()
while IFS= read -r _name; do [[ -n "$_name" ]] && SKILLS_ALLOWLIST+=("$_name"); done \
  < <("${GO_CMD[@]}" run ./cmd/manifestgen --print-allowlist)

build_skills_bundle() {
  # Deterministic archives require GNU tar's long flags; macOS bsdtar lacks them.
  if ! command -v gtar >/dev/null 2>&1; then
    echo -e "${RED}gtar (GNU tar) required for deterministic skills archives.${NC}"
    echo -e "${RED}  macOS: brew install gnu-tar   Debian/Ubuntu: apt install tar${NC}"
    exit 1
  fi
  if [[ ${#SKILLS_ALLOWLIST[@]} -eq 0 ]]; then
    echo -e "${RED}skills allowlist empty — manifestgen --print-allowlist failed${NC}"
    exit 1
  fi
  rm -rf "$SKILLS_STAGE"
  mkdir -p "$SKILLS_STAGE"
  for name in "${SKILLS_ALLOWLIST[@]}"; do
    local src="$SKILLS_SRC/$name"
    if [[ ! -f "$src/SKILL.md" ]]; then
      echo -e "${RED}skills bundle: missing $name/SKILL.md in $SKILLS_SRC${NC}"
      exit 1
    fi
    # Symlinks make dirSHA256 non-deterministic across machines; reject at source.
    if find "$src" -type l | grep -q .; then
      echo -e "${RED}skills bundle: $name contains symlink(s); not allowed${NC}"
      exit 1
    fi
    cp -R "$src" "$SKILLS_STAGE/$name"
  done
  find "$SKILLS_STAGE" \( -name '.DS_Store' -o -name '._*' \) -delete
  ( cd "$SKILLS_STAGE" && gtar \
      --sort=name --mtime='UTC 2020-01-01' \
      --owner=0 --group=0 --numeric-owner \
      --exclude='.DS_Store' --exclude='._*' \
      -czf "$BUILD_DIR/skills.tar.gz" . )
  ( cd "$BUILD_DIR" && shasum -a 256 skills.tar.gz | awk '{print $1}' > skills.tar.gz.sha256 )
  "${GO_CMD[@]}" run ./cmd/manifestgen \
    --skills-dir "$SKILLS_STAGE" --cli-version "$CLI_VERSION" \
    --min-cli-version "${SKILLS_MIN_CLI_VERSION:-}" \
    --tarball "$BUILD_DIR/skills.tar.gz" --out "$BUILD_DIR/manifest.json"
  rm -rf "$SKILLS_STAGE"
  echo -e "${GREEN}Skills bundle → skills.tar.gz / .sha256 / manifest.json${NC}"
}
build_skills_bundle

echo ""
if [[ $failed -eq 0 ]]; then
  echo -e "${GREEN}All platforms compiled → build/cli/${NC}"
  ls -lh "$BUILD_DIR"
else
  echo -e "${RED}Some platforms failed to compile${NC}"
  exit 1
fi
