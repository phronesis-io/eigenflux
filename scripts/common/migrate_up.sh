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

export PG_DSN=${PG_DSN:-postgres://eigenflux:eigenflux123@localhost:5432/eigenflux?sslmode=disable}

if command -v mise >/dev/null 2>&1 && [[ -f "$PROJECT_ROOT/mise.toml" ]]; then
  GO_CMD=(mise exec -- go)
else
  GO_CMD=(go)
fi

"${GO_CMD[@]}" run "$PROJECT_ROOT/scripts/common/migration_preflight.go"
"${GO_CMD[@]}" run github.com/pressly/goose/v3/cmd/goose@v3.24.3 -dir "$PROJECT_ROOT/migrations" postgres "$PG_DSN" up
"${GO_CMD[@]}" run "$PROJECT_ROOT/scripts/common/agent_short_id_backfill.go"
"${GO_CMD[@]}" run "$PROJECT_ROOT/scripts/common/agent_influence_backfill.go"
