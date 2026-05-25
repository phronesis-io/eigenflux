# Scripts

## Requeue Scripts

Requeue scripts recover failed pipeline items by resetting their status and republishing them to Redis Streams for the consumer to reprocess.

All scripts require infrastructure services (PostgreSQL, Redis) to be running. Build with `go build -o build/<name> ./scripts/<name>/`.

### item_requeue

Requeue failed items (status=2) back into `stream:item:publish` for full pipeline reprocessing (embedding, safety check, LLM extraction, ES indexing).

```bash
# Build
go build -o build/item_requeue ./scripts/item_requeue/

# Dry-run: show failed items from last 3 days
./build/item_requeue --days=3 --dry-run

# Requeue failed items from last 3 days
./build/item_requeue --days=3

# Requeue specific items
./build/item_requeue --item-ids=123,456,789

# Limit number of items
./build/item_requeue --days=7 --limit=100
```

| Flag | Default | Description |
|------|---------|-------------|
| `--days` | 3 | Look-back window in days (based on `raw_items.created_at`) |
| `--item-ids` | | Comma-separated item IDs (overrides `--days` filter) |
| `--limit` | 0 | Max items to requeue (0 = no limit) |
| `--dry-run` | false | Print matched items without requeueing |

### user_requeue

Requeue failed user profiles (status=2) back into `stream:profile:update` for full profile consumer reprocessing (LLM keyword extraction, embedding).

```bash
# Build
go build -o build/user_requeue ./scripts/user_requeue/

# Dry-run: show failed profiles from last 3 days
./build/user_requeue --days=3 --dry-run

# Requeue failed profiles from last 3 days
./build/user_requeue --days=3

# Requeue specific agents
./build/user_requeue --agent-ids=123,456
```

| Flag | Default | Description |
|------|---------|-------------|
| `--days` | 3 | Look-back window in days (based on `agent_profiles.updated_at`) |
| `--agent-ids` | | Comma-separated agent IDs (overrides `--days` filter) |
| `--limit` | 0 | Max profiles to requeue (0 = no limit) |
| `--dry-run` | false | Print matched profiles without requeueing |

### profile_requeue

Backfill or regenerate profile keywords using LLM. Supports two modes:

- **Default mode**: Runs LLM keyword extraction in-script with concurrent workers
- **Republish mode** (`--republish`): Resets status and publishes to `stream:profile:update`, letting the consumer handle the full pipeline

```bash
# Build
go build -o build/profile_requeue ./scripts/profile_requeue/

# Backfill keywords for all completed profiles (in-script LLM)
./build/profile_requeue --all --statuses=3 --dry-run
./build/profile_requeue --all --statuses=3

# Republish failed profiles to consumer pipeline
./build/profile_requeue --all --statuses=2 --republish --dry-run
./build/profile_requeue --all --statuses=2 --republish

# Backfill specific agents
./build/profile_requeue --agent-ids=123,456
```

| Flag | Default | Description |
|------|---------|-------------|
| `--all` | false | Process all matching profiles (required if `--agent-ids` not set) |
| `--agent-ids` | | Comma-separated agent IDs |
| `--statuses` | 3 | Comma-separated profile statuses to include |
| `--limit` | 0 | Max profiles to process (0 = no limit) |
| `--workers` | 8 | Concurrent LLM workers (default mode only) |
| `--pause` | 100ms | Per-worker sleep after each LLM call (default mode only) |
| `--update-country` | false | Also overwrite country from LLM result (default mode only) |
| `--republish` | false | Reset status and republish to stream instead of in-script LLM |
| `--dry-run` | false | Print matched profiles without processing |

### suggestion_requeue

Regenerate action suggestions for completed items using LLM.

```bash
# Build
go build -o build/suggestion_requeue ./scripts/suggestion_requeue/

# Regenerate suggestions for all completed items in last 7 days
./build/suggestion_requeue --all --dry-run
./build/suggestion_requeue --all

# Specific items
./build/suggestion_requeue --item-ids=123,456
```

| Flag | Default | Description |
|------|---------|-------------|
| `--all` | false | Process all matching items (required if `--item-ids` not set) |
| `--item-ids` | | Comma-separated item IDs |
| `--days` | 7 | Look-back window in days |
| `--limit` | 0 | Max items to process (0 = no limit) |
| `--workers` | 4 | Concurrent LLM workers |
| `--pause` | 200ms | Per-worker sleep after each LLM call |
| `--dry-run` | false | Print matched items without processing |
