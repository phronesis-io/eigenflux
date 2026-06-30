#!/bin/bash
set -e

# ============================================================
# release-skills.sh — ship a skills change WITHOUT a CLI release.
#
# Builds ONLY the skills bundle (no binary cross-compile) and uploads it to
# R2 `skills/latest/`. Clients compare the manifest `revision` (content hash),
# not the CLI version, so editing a skill is: edit -> ./release-skills.sh.
# The CLI binary is untouched.
#
# Bump SKILLS_MIN_CLI_VERSION in .cli.config ONLY if the edited skills start
# requiring a newer CLI command.
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$CLI_DIR/.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build/cli"

source "$CLI_DIR/.cli.config"
[[ -f "$CLI_DIR/.cli.env" ]] && source "$CLI_DIR/.cli.env"

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'

if [[ -z "$R2_ACCESS_KEY_ID" || -z "$R2_SECRET_ACCESS_KEY" ]]; then
  echo -e "${RED}R2 credentials missing in .cli.env${NC}"; exit 1
fi
if ! command -v gtar >/dev/null 2>&1; then
  echo -e "${RED}gtar (GNU tar) required: brew install gnu-tar${NC}"; exit 1
fi

if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go); else GO_CMD=(go); fi

SKILLS_SRC="$PROJECT_ROOT/skills"
SKILLS_STAGE="$BUILD_DIR/skills-stage"
mkdir -p "$BUILD_DIR"

# Allowlist from the single Go source of truth.
SKILLS_ALLOWLIST=()
while IFS= read -r _n; do [[ -n "$_n" ]] && SKILLS_ALLOWLIST+=("$_n"); done \
  < <(cd "$CLI_DIR" && "${GO_CMD[@]}" run ./cmd/manifestgen --print-allowlist)

echo -e "${CYAN}Staging ${#SKILLS_ALLOWLIST[@]} skills...${NC}"
rm -rf "$SKILLS_STAGE"; mkdir -p "$SKILLS_STAGE"
for name in "${SKILLS_ALLOWLIST[@]}"; do
  [[ -f "$SKILLS_SRC/$name/SKILL.md" ]] || { echo -e "${RED}missing $name/SKILL.md${NC}"; exit 1; }
  find "$SKILLS_SRC/$name" -type l | grep -q . && { echo -e "${RED}$name has symlinks${NC}"; exit 1; }
  cp -R "$SKILLS_SRC/$name" "$SKILLS_STAGE/$name"
done
find "$SKILLS_STAGE" \( -name '.DS_Store' -o -name '._*' \) -delete

( cd "$SKILLS_STAGE" && gtar --sort=name --mtime='UTC 2020-01-01' \
    --owner=0 --group=0 --numeric-owner --exclude='.DS_Store' --exclude='._*' \
    -czf "$BUILD_DIR/skills.tar.gz" . )
( cd "$BUILD_DIR" && shasum -a 256 skills.tar.gz | awk '{print $1}' > skills.tar.gz.sha256 )
( cd "$CLI_DIR" && "${GO_CMD[@]}" run ./cmd/manifestgen \
    --skills-dir "$SKILLS_STAGE" --cli-version "$CLI_VERSION" \
    --min-cli-version "${SKILLS_MIN_CLI_VERSION:-}" \
    --tarball "$BUILD_DIR/skills.tar.gz" --out "$BUILD_DIR/manifest.json" )
rm -rf "$SKILLS_STAGE"

REVISION=$(grep -o '"revision"[^,]*' "$BUILD_DIR/manifest.json" | head -1)
echo -e "${CYAN}Built skills bundle (${REVISION}); publishing to R2 skills/latest...${NC}"

export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
S3_ARGS="--endpoint-url $R2_ENDPOINT"
for f in skills.tar.gz skills.tar.gz.sha256 manifest.json; do
  aws s3 cp "$BUILD_DIR/$f" "s3://$R2_BUCKET/skills/latest/$f" $S3_ARGS --quiet
  aws s3 cp "$BUILD_DIR/$f" "s3://$R2_BUCKET/cli/latest/$f"    $S3_ARGS --quiet
  echo -e "${GREEN}  $f → skills/latest + cli/latest${NC}"
done
echo -e "${GREEN}Done. Skills are live on R2 — clients pick them up on next sync. No CLI release.${NC}"
