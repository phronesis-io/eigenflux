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

# Use bounded batches compatible with the system Bash 3.2 on macOS.
RELEASE_WORKERS="${EIGENFLUX_RELEASE_WORKERS:-3}"
if [[ ! "$RELEASE_WORKERS" =~ ^[1-8]$ ]]; then
  echo 'EIGENFLUX_RELEASE_WORKERS must be an integer from 1 to 8' >&2
  exit 1
fi
export EIGENFLUX_RELEASE_WORKERS="$RELEASE_WORKERS"
release_active_pids=()
release_worker_phase=0
release_cancel_status=0
stop_publication() {
  release_cancel_status="$1"
  trap '' INT TERM
  # Defer during dispatch/wait so a signal cannot miss a just-started child.
  if [[ $release_worker_phase -eq 0 ]]; then exit "$release_cancel_status"; fi
}
wait_release_workers() {
  local pid status=0
  for pid in "${release_active_pids[@]}"; do
    if wait "$pid"; then :; else
      # A trapped signal interrupts wait without necessarily ending the child.
      if [[ $release_cancel_status -ne 0 ]]; then wait "$pid" || true; fi
      status=1
    fi
  done
  release_active_pids=()
  return "$status"
}
run_bounded() {
  local operation="$1" item status=0
  shift
  release_worker_phase=1
  for item in "$@"; do
    [[ $release_cancel_status -eq 0 ]] || break
    "$operation" "$item" &
    release_active_pids+=("$!")
    if [[ ${#release_active_pids[@]} -ge $RELEASE_WORKERS ]]; then
      if wait_release_workers; then :; else status=1; fi
      [[ $status -eq 0 && $release_cancel_status -eq 0 ]] || break
    fi
  done
  if wait_release_workers; then :; else status=1; fi
  release_worker_phase=0
  if [[ $release_cancel_status -ne 0 ]]; then exit "$release_cancel_status"; fi
  return "$status"
}

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
trap 'stop_publication 130' INT
trap 'stop_publication 143' TERM
VERSION_FILES=()
while IFS= read -r name; do
  VERSION_FILES+=("$BUILD_DIR/$name")
done <<< "$ARTIFACTS"
VERSION_FILES+=("$BUILD_DIR/version.txt" "$BUILD_DIR/release.json" "$BUILD_DIR/package-release.json")
if [[ -f "$BUILD_DIR/release-v2.json" ]]; then
  VERSION_FILES+=("$BUILD_DIR/release-v2.json" "$BUILD_DIR/package-release-v2.json")
fi
PUBLISH_LEGACY=true
if [[ -f "$BUILD_DIR/release-v2.json" && "$NOTIFIER_VERSION" != "$CLI_VERSION" ]]; then
  PUBLISH_LEGACY=false
fi
preflight_object() {
  local file="$1" name
  name=$(basename "$file")
  if aws s3api get-object --bucket "$R2_BUCKET" --key "cli/$CLI_VERSION/$name" \
      "$CHECK_DIR/$name" $S3_ARGS > /dev/null 2> "$CHECK_DIR/$name.error"; then
    cmp -s "$file" "$CHECK_DIR/$name" || {
      echo "Published $CLI_VERSION/$name differs; bump CLI_VERSION" >&2
      return 1
    }
  elif grep -q 'An error occurred (NoSuchKey) when calling the GetObject operation' "$CHECK_DIR/$name.error"; then
    touch "$CHECK_DIR/$name.missing"
  else
    cat "$CHECK_DIR/$name.error" >&2
    return 1
  fi
}
upload_object() {
  local file="$1" name
  name=$(basename "$file")
  if [[ -f "$CHECK_DIR/$name.missing" ]]; then
    if ! aws s3api put-object --bucket "$R2_BUCKET" --key "cli/$CLI_VERSION/$name" \
        --body "$file" --if-none-match '*' $S3_ARGS > /dev/null; then
      aws s3api get-object --bucket "$R2_BUCKET" --key "cli/$CLI_VERSION/$name" \
        "$CHECK_DIR/$name" $S3_ARGS > /dev/null || return 1
      cmp -s "$file" "$CHECK_DIR/$name" || {
        echo "Published $CLI_VERSION/$name differs; bump CLI_VERSION" >&2
        return 1
      }
    fi
  fi
}
upload_alias() {
  local name="$1" file="$BUILD_DIR/$1"
  # Resume interrupted publication without re-uploading identical mutable payloads.
  if aws s3api get-object --bucket "$R2_BUCKET" --key "cli/latest/$name" \
      "$CHECK_DIR/alias-$name" $S3_ARGS > /dev/null 2> "$CHECK_DIR/alias-$name.error"; then
    if cmp -s "$file" "$CHECK_DIR/alias-$name"; then return 0; fi
  elif ! grep -q 'An error occurred (NoSuchKey) when calling the GetObject operation' "$CHECK_DIR/alias-$name.error"; then
    cat "$CHECK_DIR/alias-$name.error" >&2
    return 1
  fi
  aws s3 cp "$file" "s3://$R2_BUCKET/cli/latest/$name" $S3_ARGS --quiet
}

# No writes are allowed until every immutable object has passed preflight.
run_bounded preflight_object "${VERSION_FILES[@]}"
run_bounded upload_object "${VERSION_FILES[@]}"
if [[ "$PUBLISH_LEGACY" == true ]]; then
  ALIAS_FILES=()
  while IFS= read -r name; do ALIAS_FILES+=("$name"); done <<< "$ARTIFACTS"
  run_bounded upload_alias "${ALIAS_FILES[@]}"
fi

python3 "$SCRIPT_DIR/cli-artifacts.py" verify-public "$BUILD_DIR" \
  --base-url "$R2_PUBLIC_URL/cli/$CLI_VERSION"
if [[ "$PUBLISH_LEGACY" == true ]]; then
  python3 "$SCRIPT_DIR/cli-artifacts.py" verify-public "$BUILD_DIR" \
    --base-url "$R2_PUBLIC_URL/cli/latest"
fi
python3 "$SCRIPT_DIR/cli-artifacts.py" verify-release-public "$BUILD_DIR" \
  --base-url "$R2_PUBLIC_URL/cli/$CLI_VERSION"

# Promote signed metadata after immutable payload verification. Old clients stay on
# the bridge when native versions diverge; v2 clients download versioned payloads.
if [[ -f "$BUILD_DIR/release-v2.json" ]]; then
  aws s3 cp "$BUILD_DIR/release-v2.json" "s3://$R2_BUCKET/cli/v2/latest/release.json" $S3_ARGS --cache-control no-store --quiet
  aws s3 cp "$BUILD_DIR/package-release-v2.json" "s3://$R2_BUCKET/cli/v2/latest/package-release.json" $S3_ARGS --cache-control no-store --quiet
  aws s3 cp "$BUILD_DIR/version.txt" "s3://$R2_BUCKET/cli/v2/latest/version.txt" $S3_ARGS --cache-control no-store --quiet
fi
if [[ "$PUBLISH_LEGACY" == true ]]; then
  aws s3 cp "$BUILD_DIR/version.txt" "s3://$R2_BUCKET/cli/latest/version.txt" $S3_ARGS --cache-control no-store --quiet
  aws s3 cp "$BUILD_DIR/release.json" "s3://$R2_BUCKET/cli/latest/release.json" $S3_ARGS --cache-control no-store --quiet
  aws s3 cp "$BUILD_DIR/package-release.json" "s3://$R2_BUCKET/cli/latest/package-release.json" $S3_ARGS --cache-control no-store --quiet
fi

# Skills use the serialized Linux Release Skills workflow after CLI publication.
echo -e "${CYAN}skills: ${GREEN}published separately by Release Skills workflow${NC}"

echo ""
echo -e "${GREEN}Published to ${R2_PUBLIC_URL}/cli/${CLI_VERSION}/${NC}"
if [[ -f "$BUILD_DIR/release-v2.json" ]]; then
  echo -e "${GREEN}Latest v2 metadata at ${R2_PUBLIC_URL}/cli/v2/latest/${NC}"
fi
if [[ "$PUBLISH_LEGACY" == true ]]; then
  echo -e "${GREEN}Legacy bridge at ${R2_PUBLIC_URL}/cli/latest/${NC}"
fi
if [[ "$PUBLISH_SKILLS_AFTER_CLI" == "true" ]]; then
  bash "$SCRIPT_DIR/release-skills.sh"
fi
