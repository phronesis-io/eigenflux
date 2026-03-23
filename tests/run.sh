#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/.."; pwd)"
cd "$PROJECT_ROOT"

usage() {
  cat <<'EOF'
Usage:
  ./tests/run.sh [<dir_shortcut>] [--dir <test_pkg>] [--case <regex>] [--skip-start] [extra go test args...]

Examples:
  ./tests/run.sh
  ./tests/run.sh e2e
  ./tests/run.sh --dir ./tests/e2e
  ./tests/run.sh --dir e2e
  ./tests/run.sh --case TestE2EFullFlow
  ./tests/run.sh --dir ./tests/auth --case TestAuthLoginFlow -count=1
EOF
}

TEST_DIR="./tests/..."
TEST_CASE=""
SKIP_START="false"
EXTRA_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    -d|--dir)
      [[ $# -lt 2 ]] && echo "missing value for $1" >&2 && exit 1
      TEST_DIR="$2"
      shift 2
      ;;
    -c|--case)
      [[ $# -lt 2 ]] && echo "missing value for $1" >&2 && exit 1
      TEST_CASE="$2"
      shift 2
      ;;
    --skip-start)
      SKIP_START="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      # Shorthand: first free arg like "e2e" means --dir e2e.
      if [[ "$TEST_DIR" == "./tests/..." ]] && [[ "$1" != -* ]]; then
        TEST_DIR="$1"
      else
        EXTRA_ARGS+=("$1")
      fi
      shift
      ;;
  esac
done

# Normalize common shorthand such as "e2e" or "tests/e2e".
if [[ "$TEST_DIR" != ./* ]]; then
  if [[ -d "$PROJECT_ROOT/tests/$TEST_DIR" ]]; then
    TEST_DIR="./tests/$TEST_DIR"
  elif [[ -d "$PROJECT_ROOT/$TEST_DIR" ]]; then
    TEST_DIR="./$TEST_DIR"
  fi
fi

# Prefer project-pinned Go via mise when available.
if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

if [[ "$SKIP_START" != "true" ]]; then
  export APP_ENV=test
  ./scripts/local/start_local.sh
  ./console/console_api/scripts/start.sh
fi

export APP_ENV=test
CMD=("${GO_CMD[@]}" test -v -count=1 "$TEST_DIR")
if [[ -n "$TEST_CASE" ]]; then
  CMD+=(-run "$TEST_CASE")
fi
if [[ ${#EXTRA_ARGS[@]} -gt 0 ]]; then
  CMD+=("${EXTRA_ARGS[@]}")
fi

echo "Running: ${CMD[*]}"
"${CMD[@]}"
