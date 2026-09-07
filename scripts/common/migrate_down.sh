#!/bin/bash
set -e

PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
cd "$PROJECT_ROOT"

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

PG_DSN=${PG_DSN:-postgres://eigenflux:eigenflux123@localhost:5432/eigenflux?sslmode=disable}
TARGET_VERSION=${1:-}

if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

MIGRATION_PGOPTIONS="${PGOPTIONS:+${PGOPTIONS} }-c lock_timeout=5s -c statement_timeout=30min"
if [[ -n "$TARGET_VERSION" ]]; then
  PGOPTIONS="$MIGRATION_PGOPTIONS" "${GO_CMD[@]}" run github.com/pressly/goose/v3/cmd/goose@v3.24.3 -dir "$PROJECT_ROOT/migrations" postgres "$PG_DSN" down-to "$TARGET_VERSION"
else
  PGOPTIONS="$MIGRATION_PGOPTIONS" "${GO_CMD[@]}" run github.com/pressly/goose/v3/cmd/goose@v3.24.3 -dir "$PROJECT_ROOT/migrations" postgres "$PG_DSN" down
fi
