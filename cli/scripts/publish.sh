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

export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"

S3_ARGS="--endpoint-url $R2_ENDPOINT"

echo -e "${CYAN}Publishing eigenflux CLI v${CLI_VERSION} to R2${NC}"
echo ""

for file in "$BUILD_DIR"/eigenflux-*; do
  name=$(basename "$file")
  echo -ne "${CYAN}Uploading $name ...${NC} "

  # Upload to versioned path
  aws s3 cp "$file" "s3://$R2_BUCKET/cli/$CLI_VERSION/$name" $S3_ARGS --quiet && \
    echo -ne "${GREEN}v${CLI_VERSION} ${NC}"

  # Also upload to latest/
  aws s3 cp "$file" "s3://$R2_BUCKET/cli/latest/$name" $S3_ARGS --quiet && \
    echo -e "${GREEN}latest${NC}"
done

# Upload version.txt
aws s3 cp "$BUILD_DIR/version.txt" "s3://$R2_BUCKET/cli/latest/version.txt" $S3_ARGS --quiet
aws s3 cp "$BUILD_DIR/version.txt" "s3://$R2_BUCKET/cli/$CLI_VERSION/version.txt" $S3_ARGS --quiet

echo ""
echo -e "${GREEN}Published to ${R2_PUBLIC_URL}/cli/${CLI_VERSION}/${NC}"
echo -e "${GREEN}Latest at ${R2_PUBLIC_URL}/cli/latest/${NC}"
