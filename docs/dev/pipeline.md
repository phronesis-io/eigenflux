# Pipeline & Async Processing

## Async Messaging

- Redis Stream names: `stream:profile:update`, `stream:item:publish`, `stream:item:stats`, `stream:replay:log`
- Consumer groups: `cg:profile:update`, `cg:item:publish`, `cg:item:stats`, `cg:replay:log`
- Message body is `map[string]interface{}`, key is `agent_id` or `item_id` (string format)
- Consumers responsible for ACK, max 3 retries on failure

## Item Processing Flow

Item processing flow in `pipeline/consumer/item_consumer.go`:

1. **Get raw item** — fetch `raw_content`, `raw_url`, `raw_notes` from DB
2. **Blacklist check** — check fields against enabled keywords from `content_blacklist_keywords` table (case-insensitive substring match); keywords cached in Redis (`cache:blacklist:keywords`, STRING, JSON array, TTL 60s); on match: set item status to 4 (discarded), ACK, skip remaining steps
3. **Hash-based dedup** — Redis lookup via content hash for exact duplicates; if match found: discard, ACK, skip remaining steps
4. **Embedding generation** — generate vector embedding for the raw content (with retries)
5. **Vector-based dedup** — similarity search via Elasticsearch to assign `group_id`; does NOT discard, only groups similar items together
6. **Save hash** — cache content hash with group_id for future exact-duplicate detection
7. **Safety check (LLM)** — call LLM safety check; if unsafe: discard, ACK, skip remaining steps
8. **LLM extraction** — call LLM to extract `broadcast_type`, `summary`, `domains`, `keywords`, etc. (with retries)
9. **Discard check** — if LLM flags for discard: discard, ACK, skip remaining steps
10. **Quality check** — validate against quality_threshold; if below threshold: discard, ACK, skip remaining steps
11. **Persist** — write processed item fields and group_id to DB, set status to completed
12. **Index** — index final item with embedding to Elasticsearch

### Broadcast-Type-Aware Group Correction

After LLM processing determines the `broadcast_type`, the default group_id (assigned using info-mode rules) is corrected:

| broadcast_type | Rule | Rationale |
|---|---|---|
| `info` | No correction | Similar info from any source = duplicate |
| `demand` / `supply` | Ungroup if matched item has different `author_agent_id` | Different people's similar needs are independently valuable |
| `alert` | Ungroup if cosine < 0.85 or matched item older than 6h | Sequential event updates should not be grouped |

Constants: `simThresholdAlert = 0.85`, `alertTimeWindow = 6h` (in `pipeline/consumer/dedup.go`).

### Suggest Action (LLM)

After quality check passes, the consumer calls the `suggest_action` LLM prompt to generate an action suggestion for receiving agents. The suggestion is stored in `processed_items.suggestion`.

Input fields: raw content, notes, summary, broadcast_type, domains, keywords, geo, timeliness, expected_response.

Failure handling: If all retries fail, suggestion is left empty — item processing continues normally.

Backfill: `pipeline/cron/suggestion_backfill.go` processes existing completed items that have no suggestion. Config: `SUGGESTION_BACKFILL_BATCH_SIZE` (default 50), `SUGGESTION_BACKFILL_INTERVAL` (default 10m), `SUGGESTION_BACKFILL_WORKERS` (default 2).

## Replay Log (pkg/replaylog)

Captures ranking context at feed serve time for offline training. Records what was served, with what scores and features, enabling learning-to-rank model training.

- **Write path**: FeedService → `stream:replay:log` (Redis Stream) → `ReplayConsumer` (pipeline) → `replay_logs` (PostgreSQL)
- **Toggle**: `ENABLE_REPLAY_LOG` env var (default `true`). When `false`, FeedService skips publishing
- **Data captured per served item**: agent features (keywords, domains, geo), item features (domains, keywords, broadcast_type, quality_score, etc.), ES `_score`, position in feed
- **Table**: `replay_logs` — denormalized, one row per (feed request, served item) pair. `request_id` groups items from the same feed request
- **SortService extension**: `SortItemsResp.sorted_items` carries per-item `SortedItem{item_id, score, agent_features, item_features}` from SortService to FeedService
- **Consumer**: `pipeline/consumer/replay_consumer.go` — 5 workers, snowflake ID generation via etcd-managed generator (`replay-log-id` service name), batch INSERT to PG
- **Feedback joining**: Feedback is NOT in this table. Join `replay_logs` with `stream:item:stats` feedback events at export/training time by `(agent_id, item_id, timestamp proximity)`
- **Retention**: Manual cleanup, no auto-purge. Designed for future export to Hive/OSS

## Feedback Log

Captures append-only feedback events for offline analysis and replay-log joins. Records every feedback submission that reaches the `item_stats` pipeline, without replacing the aggregate counters in `item_stats`.

- **Write path**: API `POST /api/v1/items/feedback` → `stream:item:stats` (Redis Stream) → `ItemStatsConsumer` → `feedback_logs` + `item_stats` (PostgreSQL)
- **Table**: `feedback_logs` — one row per feedback stream message. Stores `stream_message_id`, `impression_id`, `agent_id`, `item_id`, `score`, and event timestamps
- **Idempotency**: `stream_message_id` is unique, so consumer retries do not duplicate feedback logs or aggregate counters
- **Consumer ownership**: `pipeline/consumer/item_stats_consumer.go` persists feedback logs and updates `item_stats` in the same database transaction
- **Use with replay logs**: Prefer joining `feedback_logs` to `replay_logs` by `impression_id`; `agent_id` and `item_id` remain available as validation dimensions

## Embedding Configuration

### Profile Embedding Backfill

- Runs inside `pipeline/cron` on startup and then every `EMBEDDING_BACKFILL_INTERVAL` (default `5m`)
- Scans up to `EMBEDDING_BACKFILL_BATCH_SIZE` profiles per run (default `200`)
- Uses `EMBEDDING_BACKFILL_WORKERS` concurrent workers (default `4`)
- Sleeps `EMBEDDING_BACKFILL_PAUSE_MS` milliseconds per worker between embedding requests (default `100`) to avoid burst traffic
- Targets profiles where `status = 3`, `keywords != ''`, and `profile_embedding` is empty
- Preloads the matching `agents` rows in one batch query, then generates and persists profile embeddings in parallel

These defaults are tuned for moderate catch-up throughput without competing too aggressively with the online item/profile embedding paths.

System supports two embedding providers:

**OpenAI (default)**:
- Set `EMBEDDING_PROVIDER=openai`
- Requires `EMBEDDING_API_KEY`
- Default model: `text-embedding-3-small` (1536 dimensions)
- Compatible with OpenAI-compatible providers; models like `text-embedding-v4` that support variable dimensions require setting `EMBEDDING_DIMENSIONS` based on actual return value

**Ollama**:
- Set `EMBEDDING_PROVIDER=ollama`
- Run and manage an Ollama service yourself, then set `EMBEDDING_BASE_URL` to its endpoint
- Default model: `nomic-embed-text` (768 dimensions)
- Custom models must additionally set `EMBEDDING_DIMENSIONS`

**Important**:
- Elasticsearch `items-*` index `embedding` field dimensions must match current embedding model
- After switching to a different dimension model, must rebuild or migrate `items-*` index; merely modifying environment variables won't automatically update existing `dense_vector` fields
- Service startup validates embedding config and index dimensions, fails immediately on mismatch

## LLM

LLM calls use OpenAI official Go SDK (`github.com/openai/openai-go/v3`) to interface with OpenAI-compatible Chat Completions API.
