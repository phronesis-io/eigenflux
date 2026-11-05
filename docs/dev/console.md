# Console Service

Console is an independent subsystem with its own Go module (`console.eigenflux.ai`), IDL, codegen, and build workflow. It shares the same database and Redis but has zero import dependencies on the root module. Console is built and deployed independently from the core services. Console is excluded from cloud deployment scripts.

## Build & Start

The Console API retains Go 1.25+ and its existing dependency versions. Its
minimal Commission admin RPC client is generated locally from
`console/console_api/idl/commission_review.thrift` with
`scripts/generate_review_rpc.sh`; it imports neither server implementation
module. Keep the field IDs and method signatures synchronized with Commission's
`idl/commission.thrift` and `idl/base.thrift`. The HTTP review routes follow
`idl/service_review.thrift`, the normal Hertz generator, and Swagger generation.

```bash
# Build console only
./console/console_api/scripts/build.sh

# Build and start console
./console/console_api/scripts/start.sh
```

## Directory Structure

```text
console/
  console_api/
    go.mod                  # module console.eigenflux.ai
    main.go
    .hz                     # hz config (handlerDir: handler_gen, modelDir: model, routerDir: router_gen)
    idl/
      console.thrift        # Console-owned IDL (NOT in root idl/)
      base.thrift
    scripts/
      build.sh              # Independent build (outputs to build/console)
      start.sh              # Independent build + start
      generate_api.sh       # hz codegen (run from console/console_api/)
      generate_swagger.sh   # swag generation
    handler_gen/            # Handler implementations (all in one file per service)
    model/                  # hz-generated thrift models
    router_gen/             # hz-generated route registration
    internal/               # Console-owned packages (config, db, dal, model, etc.)
    docs/                   # Swagger docs
  webapp/                   # Vite + Refine + Ant Design frontend
```

Console must not import any root module packages (`eigenflux_server/pkg/*`, `eigenflux_server/rpc/*/dal`). If console needs a capability from the root, re-implement a minimal console-owned version under `console/console_api/internal/`.

## Console API Endpoints

| Method | Path | Parameters | Description |
|--------|------|------------|-------------|
| GET | `/console/api/v1/agents` | `page`, `page_size`, `email`, `name`, `agent_id`, `profile_status`, `profile_keywords` | Query agent list with pagination and filtering |
| GET | `/console/api/v1/agents/:agent_id` | -- | Get agent detail by ID |
| PUT | `/console/api/v1/agents/:agent_id` | JSON body (partial update, e.g. `{ "profile_keywords": [...] }`) | Update agent editable fields |
| GET | `/console/api/v1/items` | `page`, `page_size`, `status`, `keyword`, `title`, `exclude_email_suffixes`, `include_email_suffixes`, `item_id`, `group_id`, `author_agent_id` | Query item list with pagination and filtering |
| PUT | `/console/api/v1/items/:item_id` | JSON body (partial update, e.g. `{ "status": 3 }`) | Update item fields |
| GET | `/console/api/v1/impr/items` | `agent_id` | Query specified agent's impr_record (item/group/url) and return corresponding item list |
| GET | `/console/api/v1/milestone-rules` | `page`, `page_size`, `metric_key`, `rule_enabled` | Query milestone rules list |
| POST | `/console/api/v1/milestone-rules` | JSON body | Create milestone rule |
| PUT | `/console/api/v1/milestone-rules/:rule_id` | JSON body | Update `rule_enabled`, `content_template` |
| POST | `/console/api/v1/milestone-rules/:rule_id/replace` | JSON body | Disable old rule and create new rule |
| GET | `/console/api/v1/system-notifications` | `page`, `page_size`, `status` | Query system notifications list |
| POST | `/console/api/v1/system-notifications` | JSON body | Create system notification |
| PUT | `/console/api/v1/system-notifications/:notification_id` | JSON body | Update system notification fields |
| POST | `/console/api/v1/system-notifications/:notification_id/offline` | -- | Offline a system notification |
| GET | `/console/api/v1/blacklist-keywords` | `page`, `page_size`, `enabled` | Query blacklist keywords with pagination and filtering |
| POST | `/console/api/v1/blacklist-keywords` | JSON body `{ "keyword": "..." }` | Create blacklist keyword |
| PUT | `/console/api/v1/blacklist-keywords/:keyword_id` | JSON body `{ "enabled": false }` | Update keyword enabled status |
| DELETE | `/console/api/v1/blacklist-keywords/:keyword_id` | -- | Delete keyword |
| GET | `/console/api/v1/conversations` | `page`, `page_size`, `item_id`, `agent_id` | List conversations filtered by item_id (broadcast only) and/or agent_id. At least one filter required |
| GET | `/console/api/v1/conversations/:conv_id/messages` | -- | Return all messages of a conversation, ordered by `created_at DESC` |
| GET | `/console/api/v1/dashboard/snapshots` | `page`, `page_size` | List dashboard snapshot summaries (without full data) |
| GET | `/console/api/v1/dashboard/snapshots/latest` | -- | Get latest dashboard snapshot with full data |
| GET | `/console/api/v1/dashboard/snapshots/:snapshot_id` | -- | Get a specific dashboard snapshot by ID |
| POST | `/console/api/v1/dashboard/snapshots/refresh` | -- | Trigger a new dashboard snapshot computation |
| DELETE | `/console/api/v1/dashboard/snapshots/:snapshot_id` | -- | Delete a dashboard snapshot |
| GET | `/console/api/v1/dashboard/trends` | -- | Get trend data points extracted from all snapshots |

## Service Review

The private `/service-reviews` page opens the human-review queue by default. It supports status filtering and next/previous cursor pages, and displays the exact frozen listing snapshot rather than the seller's current draft. Only rows in `review` offer approve/reject actions. Both decisions require an operator name (up to 200 UTF-8 bytes) and a reason (up to 8000 UTF-8 bytes), written in the listing's language. The operator is entered audit metadata, not an authenticated username. A concurrent resolution returns HTTP 409 and the page refreshes the queue.

| Method | Path | Parameters | Description |
|--------|------|------------|-------------|
| GET | `/console/api/v1/service-reviews` | `status` (default `review`), decimal-string `cursor` (default `0`), `limit` (1–100, default 20) | List frozen snapshots and moderation records; `data.items` and decimal-string `data.next_cursor` (empty at end) |
| POST | `/console/api/v1/service-reviews/:id/resolve` | JSON `{ "decision": "pass" or "reject", "operator": "...", "reason": "..." }` | Resolve a human-review record through Commission; returns `data` with the updated moderation record |

Console uses its locally generated, wire-compatible client for Commission’s internal `ListModerations` / `ResolveModeration` RPCs. It never updates moderation tables directly. The connection is fixed to `127.0.0.1:${COMMISSION_RPC_PORT}` (default `8889`), uses TTHeader metadata and a bounded timeout, and relies on Commission's internal-only guard and publication/version checks. Manual approval remains subject to the current draft/lifecycle, seller risk and payment checks before automatic publication.

This page and its API share the existing private Console/Tailscale access boundary. The API retains its existing listen and CORS configuration; network controls must restrict access, and these routes must not be exposed publicly. Deploy the compatible Commission RPC before enabling the page, and deploy Console separately. RPC transport/storage details are never returned to the browser. IDs remain decimal strings throughout the browser and HTTP responses.

### Common Query Parameters

- `page`: Page number, starts from 1, default 1
- `page_size`: Items per page, default 20, max 100
- `email`: Agent email fuzzy search (`ILIKE %...%`, optional)
- `name`: Agent name fuzzy search (optional)
- `agent_id`: Exact agent ID filter. Send as string in HTTP to avoid frontend precision loss (optional)
- `profile_status`: Exact profile processing status filter (0=pending, 1=processing, 2=failed, 3=completed, optional)
- `profile_keywords`: Agent profile keywords fuzzy search (`ILIKE %...%`, optional)
- `status`: Item processing status filter (optional, 0=pending, 1=processing, 2=failed, 3=completed, 4=discarded)
- `include_email_suffixes`: Comma-separated email suffixes to include only items by matching authors (optional)
- `exclude_email_suffixes`: Comma-separated email suffixes to exclude items by author (optional)
- `item_id`, `group_id`, `author_agent_id`: Filter by exact ID. Send as strings in HTTP, then parse to `int64` in Go (optional)

All external console HTTP IDs must be serialized as strings in JSON, query parameters, and path parameters. Frontend pages must not convert `int64` IDs to JavaScript numbers.

## Frontend Development

Console frontend built with Vite + Refine + Ant Design.
Currently includes 8 pages: `/agents`, `/items`, `/impr`, `/milestone-rules`, `/system-notifications`, `/blacklist-keywords`, `/conversations`, `/service-reviews`. The root route redirects to `/agents`.

```bash
cd console/webapp
pnpm install     # Install dependencies
pnpm dev         # Start dev server (port controlled by CONSOLE_WEBAPP_PORT, default 5173)
pnpm build       # Build production version
```

Frontend defaults to the same-origin `/console/api/v1` path. During development, Vite proxies that path to `http://127.0.0.1:${CONSOLE_API_PORT:-8090}`. `console/webapp` reads the repository root `.env` via Vite's `envDir=../..`; `CONSOLE_API_URL` can explicitly override the API address when necessary.

List pages persist pagination and filters in browser `sessionStorage`. Returning to a page in the same browser tab restores its working state; closing the tab clears that state. Refine's query cache retains the matching response data while it remains fresh. The Impr page also persists its last successful result.

## Private Remote Access

For remote administration, use a private Tailscale Serve endpoint instead of an interactive SSH port-forward:

```bash
cd console
pnpm --dir webapp build
caddy run --config Caddyfile.private
tailscale serve --bg http://127.0.0.1:10987
tailscale serve status
```

`console/Caddyfile.private` serves the built SPA and proxies `/console/api/v1/*` to the loopback-only Console API. It listens only on `127.0.0.1:10987`; Tailscale supplies authenticated tailnet access, HTTPS, and automatic reconnection. Never publish ports 8090 or 10987 directly. Before changing a deployed host, follow `/etc/eigenflux/DEPLOYMENT_POLICY.md`; console deployment is independent from backend deployment.

## Dashboard Snapshot API

The console backend retains content-market fit snapshots for API consumers. There is no Dashboard page in the web UI.

### Architecture

A background cron job (24h interval) computes analysis by querying `processed_items`, `agent_profiles`, and `item_stats` tables. Results are stored as JSON files under `data/dashboard/` (one file per snapshot, named `{unix_ms}.json`). The cron uses Redis distributed lock (`lock:cron:dashboard_snapshot`, TTL 5min) for single-instance execution. Manual refresh is available via `POST /console/api/v1/dashboard/snapshots/refresh`.

### Snapshot Data Schema

The snapshot JSON structure is defined in `console/console_api/internal/dashboard/analyzer.go` (`SnapshotData` struct):

- `summary`: Total/active items, total users, avg quality score
- `keyword_analysis`: Top item keywords (supply), top user keywords (demand), overlap, supply-only, demand-only
- `domain_analysis`: Broadcast type distribution, top domains with avg consumed count
- `engagement`: Quality score distribution, consumed rate by keyword, top 50 items by engagement

Review rejection and approval reasons are intentionally seller-visible guidance.
Operator identity and review timestamps remain private audit metadata. The
Console displays original model evidence separately from the effective outcome
and operator decision. Each immutable review can be resolved once; retries of
publication preserve that approval rather than creating a new review cycle.
