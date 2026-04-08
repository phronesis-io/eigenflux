---
name: eigenflux-test
description: Use when feature development, bugfix, or refactoring is complete in the EigenFlux project and code needs validation. Proactively invoke after finishing implementation — build, start services, run affected unit and integration tests autonomously.
---

# EigenFlux Test

After completing any code change, build, start services, and run all affected tests. Do NOT ask the user — all scripts are idempotent and safe.

## Execution Steps

1. **Build** — `bash scripts/common/build.sh` (add `./console/console_api/scripts/build.sh` if console changed)
2. **Start services** — `./scripts/local/start_local.sh` (add `./console/console_api/scripts/start.sh` if console changed)
3. **Run unit tests** — `go test -v ./path/to/changed/package/...` for each changed package that has `*_test.go` files
4. **Run integration tests** — `./tests/run.sh --skip-start <suite>` for each affected suite (services already running from step 2)
5. **Run e2e** — `./tests/run.sh --skip-start e2e` if changes cross module boundaries or touch shared packages

## Source → Test Mapping

Use this table to determine which test suites to run based on changed files:

| Changed directory | Unit tests (go test) | Integration tests (run.sh --skip-start) |
|---|---|---|
| `rpc/auth/` | `./rpc/auth/...` | `auth` |
| `rpc/profile/` | `./rpc/profile/...` | `e2e` |
| `rpc/item/` | `./rpc/item/...` | `item`, `e2e` |
| `rpc/sort/` | `./rpc/sort/...` | `sort` |
| `rpc/feed/` | `./rpc/feed/...` | `e2e` |
| `rpc/pm/` | `./rpc/pm/...` | `pm` |
| `rpc/notification/` | `./rpc/notification/...` | `notify`, `e2e` |
| `api/` | — | `auth`, `website`, `e2e` |
| `ws/` | — | `ws` |
| `pipeline/` | `./pipeline/...` | `pipeline` |
| `pkg/cache/` | `./pkg/cache/...` | `cache` |
| `pkg/audience/` | `./pkg/audience/...` | `notify` |
| `pkg/es/` | `./pkg/es/...` | `sort` |
| `pkg/dedup/` | `./pkg/dedup/...` | `e2e` |
| `pkg/stats/` | `./pkg/stats/...` | `website` |
| `pkg/milestone/` | `./pkg/milestone/...` | `e2e` |
| `pkg/mq/`, `pkg/config/`, `pkg/logger/` | `./pkg/<name>/...` | `e2e` |
| `console/` | `./console/...` | `console` |
| `idl/`, `kitex_gen/` | — | `e2e` |
| `tests/testutil/` | — | ALL affected suites |
| `static/templates/prompts/` | `./pipeline/llm/...` | `pipeline`, `e2e` |

**Cross-cutting rule:** If `pkg/` shared libraries change, also run integration suites of all upstream consumers. When in doubt, run `e2e`.

## Failure Handling

- If build fails → fix compilation errors first, do not proceed to tests
- If a service fails to start → check `.log/<service>.log`, fix before running tests
- If unit tests fail → fix before running integration tests
- If integration tests fail → report failures with error details, do not suppress or work around them
