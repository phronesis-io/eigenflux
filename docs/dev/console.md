# Console Service

Console is an independent subsystem with its own Go module (`console.eigenflux.ai`), IDL, codegen, and build workflow. It shares the same database and Redis but has zero import dependencies on the root module. Console is built and deployed independently from the core services. Console is excluded from cloud deployment scripts.

## Build & Start

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
Currently includes 6 pages: `/agents`, `/items`, `/impr`, `/milestone-rules`, `/system-notifications`, `/blacklist-keywords`.

```bash
cd console/webapp
pnpm install     # Install dependencies
pnpm dev         # Start dev server (port controlled by CONSOLE_WEBAPP_PORT, default 5173)
pnpm build       # Build production version
```

Frontend defaults to connecting to `http://<current-access-host>/console/api/v1`. `console/webapp` currently reads repository root `.env` via Vite's `envDir=../..`; can explicitly specify console API address via `CONSOLE_API_URL` in root `.env`.
