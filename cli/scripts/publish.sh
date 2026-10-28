#!/bin/bash
set -e

# ============================================================
# publish.sh - Upload CLI binaries to Cloudflare R2
# Usage: ./cli/scripts/publish.sh
# Requires: AWS CLI configured with R2 credentials
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$CLI_DIR/.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build/cli"

source "$CLI_DIR/.cli.config"
if [[ -f "$CLI_DIR/.cli.env" ]]; then
  source "$CLI_DIR/.cli.env"
fi

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

if [[ -z "$R2_ACCESS_KEY_ID" || -z "$R2_SECRET_ACCESS_KEY" ]]; then
  echo -e "${RED}R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY must be set in .cli.env or environment${NC}"
  exit 1
fi

# Reject incomplete or changed artifacts before making any remote writes.
ARTIFACTS=$(python3 "$SCRIPT_DIR/cli-artifacts.py" list "$BUILD_DIR")
python3 "$SCRIPT_DIR/cli-artifacts.py" verify-release "$BUILD_DIR"
bash "$SCRIPT_DIR/verify-macos-packages.sh" "$BUILD_DIR"
if [[ "$(cat "$BUILD_DIR/version.txt")" != "$CLI_VERSION" ]]; then
  echo "build/cli/version.txt does not match CLI_VERSION; rebuild before publishing" >&2
  exit 1
fi
if [[ -z "$R2_PUBLIC_URL" ]]; then
  echo "R2_PUBLIC_URL is required to verify published artifacts" >&2
  exit 1
fi

# Skills can require this release, so dispatch their publisher after the CLI pointer.
PUBLISH_SKILLS_AFTER_CLI="${EIGENFLUX_PUBLISH_SKILLS_WITH_CLI:-false}"

export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"

S3_ARGS="--endpoint-url $R2_ENDPOINT"

echo -e "${CYAN}Publishing eigenflux CLI v${CLI_VERSION} to R2${NC}"
echo ""

# Preflight the complete immutable release before writing even a missing object.
CHECK_DIR=$(mktemp -d)
trap 'rm -rf "$CHECK_DIR"' EXIT
VERSION_FILES=()
while IFS= read -r name; do
  VERSION_FILES+=("$BUILD_DIR/$name")
done <<< "$ARTIFACTS"
VERSION_FILES+=("$BUILD_DIR/version.txt" "$BUILD_DIR/release.json" "$BUILD_DIR/package-release.json")
for file in "${VERSION_FILES[@]}"; do
  name=$(basename "$file")
  if aws s3api get-object --bucket "$R2_BUCKET" --key "cli/$CLI_VERSION/$name" \
      "$CHECK_DIR/$name" $S3_ARGS > /dev/null 2> "$CHECK_DIR/error"; then
    cmp -s "$file" "$CHECK_DIR/$name" || {
      echo "Published $CLI_VERSION/$name differs; bump CLI_VERSION" >&2
      exit 1
    }
  elif grep -q 'An error occurred (NoSuchKey) when calling the GetObject operation' "$CHECK_DIR/error"; then
    touch "$CHECK_DIR/$name.missing"
  else
    cat "$CHECK_DIR/error" >&2
    exit 1
  fi
done
for file in "${VERSION_FILES[@]}"; do
  name=$(basename "$file")
  if [[ -f "$CHECK_DIR/$name.missing" ]]; then
    if ! aws s3api put-object --bucket "$R2_BUCKET" --key "cli/$CLI_VERSION/$name" \
        --body "$file" --if-none-match '*' $S3_ARGS > /dev/null; then
      aws s3api get-object --bucket "$R2_BUCKET" --key "cli/$CLI_VERSION/$name" \
        "$CHECK_DIR/$name" $S3_ARGS > /dev/null
      cmp -s "$file" "$CHECK_DIR/$name" || {
        echo "Published $CLI_VERSION/$name differs; bump CLI_VERSION" >&2
        exit 1
      }
    fi
  fi
done

while IFS= read -r name; do
  file="$BUILD_DIR/$name"
  echo -ne "${CYAN}Uploading $name ...${NC} "

  # Upload to versioned path
  # Versioned objects were conditionally created or verified above.
  echo -ne "${GREEN}v${CLI_VERSION} ${NC}"

  # Also upload to latest/
  aws s3 cp "$file" "s3://$R2_BUCKET/cli/latest/$name" $S3_ARGS --quiet
  echo -e "${GREEN}latest${NC}"
done <<< "$ARTIFACTS"

python3 "$SCRIPT_DIR/cli-artifacts.py" verify-public "$BUILD_DIR" \
  --base-url "$R2_PUBLIC_URL/cli/$CLI_VERSION"
python3 "$SCRIPT_DIR/cli-artifacts.py" verify-public "$BUILD_DIR" \
  --base-url "$R2_PUBLIC_URL/cli/latest"
python3 "$SCRIPT_DIR/cli-artifacts.py" verify-release-public "$BUILD_DIR" \
  --base-url "$R2_PUBLIC_URL/cli/$CLI_VERSION"

# Advertise the version only after all payloads are available and verified.
aws s3 cp "$BUILD_DIR/version.txt" "s3://$R2_BUCKET/cli/latest/version.txt" $S3_ARGS --quiet
aws s3 cp "$BUILD_DIR/release.json" "s3://$R2_BUCKET/cli/latest/release.json" $S3_ARGS --cache-control no-store --quiet
aws s3 cp "$BUILD_DIR/package-release.json" "s3://$R2_BUCKET/cli/latest/package-release.json" $S3_ARGS --cache-control no-store --quiet

# Skills use the serialized Linux Release Skills workflow after CLI publication.
echo -e "${CYAN}skills: ${GREEN}published separately by Release Skills workflow${NC}"

echo ""
echo -e "${GREEN}Published to ${R2_PUBLIC_URL}/cli/${CLI_VERSION}/${NC}"
echo -e "${GREEN}Latest at ${R2_PUBLIC_URL}/cli/latest/${NC}"
if [[ "$PUBLISH_SKILLS_AFTER_CLI" == "true" ]]; then
  bash "$SCRIPT_DIR/release-skills.sh"
fi
