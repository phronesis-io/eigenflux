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

## Embedding Configuration

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
