# Offline Replay Module Design

## Problem Statement

The sort pipeline (recall → rank → dedup → threshold → exploration) has many tunable parameters (alpha/beta/gamma weights, freshness curves, thresholds, recall sizes) but no way to evaluate changes offline. Currently, parameter tuning requires deploying to prod and observing online metrics — slow, risky, and hard to iterate on.

We need an offline replay module that:
1. Simulates real prod sort scenes with customizable parameters and simulated time
2. Returns ranked items with full score breakdowns for external LLM-as-judge evaluation
3. Produces zero side effects on prod data

## Architecture Decision

**Standalone Hertz HTTP service** (`replay/`) on a dedicated port (8092). It directly imports and uses `rpc/sort/ranker` and `rpc/sort/dal` packages — no RPC calls to the prod sort service.

### Why standalone over modifying prod sort service

- Complete isolation — zero risk of touching prod state
- No "replay mode" flags threaded through production code
- Can parameterize everything freely without affecting prod defaults
- Independent lifecycle — run only when needed

### What it reuses

| Package | Usage |
|---|---|
| `rpc/sort/ranker` | Scoring engine (Ranker, RankerConfig, signals, exploration) |
| `rpc/sort/dal` | ES queries (SearchItems, SearchByEmbedding, item parsing) |
| `pkg/config` | Base configuration and env var loading |
| `pkg/es` | ES client initialization |

### What it does NOT use

| Package | Why excluded |
|---|---|
| `pkg/cache` | No caching — each replay request hits ES directly for deterministic results |
| Bloom filter | Read-only access when `use_feed_history: true`, never writes |
| Redis streams | No replay log or impression recording |

## API Contract

### `POST /api/v1/replay/sort`

#### Request

```json
{
  "agent_id": 12345,
  "agent_profile": {
    "keywords": ["AI", "distributed systems"],
    "domains": ["tech", "finance"],
    "geo": "Japan",
    "embedding": [0.1, 0.2, ...]
  },
  "simulated_at": "2026-04-15T10:00:00Z",
  "use_feed_history": false,
  "limit": 20,
  "ranker_params": {
    "alpha": 0.5,
    "beta": 0.15,
    "gamma": 0.25,
    "delta": 0.1,
    "min_relevance_score": 0.05,
    "urgency_boost": 0.5,
    "urgency_window": "24h",
    "exploration_slots": 1,
    "draft_dampening": 0.8,
    "freshness": {
      "info":   {"offset": "12h", "scale": "7d", "decay": 0.8},
      "alert":  {"offset": "2h",  "scale": "12h", "decay": 0.5},
      "supply": {"offset": "48h", "scale": "30d", "decay": 0.9},
      "demand": {"offset": "12h", "scale": "7d", "decay": 0.8}
    }
  },
  "recall_params": {
    "keyword_recall_size": 200,
    "enable_knn_recall": true,
    "knn_recall_k": 80,
    "knn_recall_candidates": 300
  }
}
```

**All fields except `agent_id` are optional.** Omitted fields use prod defaults from env vars.

| Field | Default | Description |
|---|---|---|
| `agent_id` | required | Agent identifier. Used for DB profile lookup (if `agent_profile` not provided) and bloom filter key (if `use_feed_history: true`) |
| `agent_profile` | DB lookup | If provided, use this profile directly instead of looking up from DB. All sub-fields optional — omitted sub-fields filled from DB. If fully specified, no DB call needed |
| `simulated_at` | current time | The "now" for freshness scoring, expiry filtering, and ES decay queries |
| `use_feed_history` | `false` | If `true`, read prod bloom filter (read-only) to skip already-seen items. If `false`, all items are candidates |
| `limit` | 20 | Number of ranked items to return |
| `ranker_params` | prod defaults | Override any ranker weight or freshness config. Only specified fields override; rest use prod defaults |
| `recall_params` | prod defaults | Override ES recall settings. Only specified fields override; rest use prod defaults |

**Minimal request** (all prod defaults, current time, no history):
```json
{"agent_id": 12345}
```

**Single param override:**
```json
{"agent_id": 12345, "simulated_at": "2026-04-15T10:00:00Z", "ranker_params": {"alpha": 0.6}}
```

#### Response

```json
{
  "ranked_items": [
    {
      "item_id": 67890,
      "position": 0,
      "score": 0.82,
      "scores": {
        "semantic": 0.91,
        "keyword": 0.65,
        "freshness": 0.78,
        "total": 0.82,
        "is_draft": false
      },
      "item": {
        "content": "...",
        "summary": "...",
        "broadcast_type": "demand",
        "keywords": ["AI", "startup"],
        "domains": ["tech"],
        "geo": "Tokyo, Japan",
        "expire_time": "2026-04-16T10:00:00Z",
        "quality_score": 0.85,
        "updated_at": "2026-04-15T08:30:00Z",
        "source_type": "original",
        "group_id": 111
      }
    }
  ],
  "filtered_items": [],
  "exploration_items": [],
  "agent_profile": {
    "keywords": ["AI", "distributed systems"],
    "domains": ["tech", "finance"],
    "geo": "Japan",
    "has_embedding": true
  },
  "config_used": {
    "ranker_params": {
      "alpha": 0.6,
      "beta": 0.2,
      "gamma": 0.3,
      "delta": 0.1
    },
    "recall_params": {
      "keyword_recall_size": 200,
      "enable_knn_recall": true,
      "knn_recall_k": 80,
      "knn_recall_candidates": 300
    }
  },
  "stats": {
    "keyword_recall_count": 142,
    "knn_recall_count": 58,
    "merged_count": 178,
    "after_group_dedup_count": 150,
    "above_threshold_count": 45,
    "bloom_filtered_count": 0,
    "total_latency_ms": 230
  }
}
```

| Field | Description |
|---|---|
| `ranked_items` | Top items after full pipeline, with per-item score breakdown and full item content |
| `filtered_items` | Items below `min_relevance_score` threshold — included for analysis |
| `exploration_items` | Items selected via exploration slots (if `exploration_slots > 0`) |
| `agent_profile` | The profile used for ranking — useful for verifying the right agent was loaded |
| `config_used` | Merged config (overrides + defaults) echoed back for reproducibility |
| `stats` | Pipeline statistics: how many items at each stage, latency |

## Isolation Guarantees

| Prod component | `use_feed_history: false` | `use_feed_history: true` |
|---|---|---|
| Bloom filter (Redis) | Not accessed | **Read-only** — checks group_id, never writes |
| Search cache (Redis) | Not used | Not used |
| Profile cache (Redis) | Not used | Not used |
| Embedding cache (Redis) | Not used | Not used |
| Impression recording | Skipped | Skipped |
| Replay log stream | Not published to | Not published to |
| Elasticsearch | Read-only queries | Read-only queries |
| PostgreSQL | Read-only profile lookup (skipped if `agent_profile` fully provided) | Read-only profile lookup (skipped if `agent_profile` fully provided) |

## Time Simulation

The `simulated_at` timestamp is threaded through every time-dependent operation:

| Component | How `simulated_at` is used |
|---|---|
| ES keyword recall query | `updated_at <= simulated_at` filter. Gaussian decay `origin` set to `simulated_at`. Expire filter: `expire_time >= simulated_at` |
| ES kNN recall query | Same expire filter: `expire_time >= simulated_at` |
| Ranker freshness scoring | `now` parameter set to `simulated_at` for all Gaussian decay and urgency calculations |
| Exploration slot selection | "Recent" items (within 48h) judged relative to `simulated_at` |

This means replaying with `simulated_at: "2026-04-01T00:00:00Z"` produces results as if the sort happened on April 1st — items created after that date are excluded, freshness scores reflect that date, and expired items are filtered accordingly.

## Pipeline Flow

The replay pipeline mirrors the prod sort handler, minus all side effects:

```
POST /api/v1/replay/sort
  │
  ├─ Parse request, merge params with prod defaults
  │
  ├─ Resolve agent profile:
  │   ├─ If agent_profile in request → use directly (no DB call)
  │   ├─ If partially provided → merge with DB lookup
  │   └─ If omitted → load from DB (keywords, domains, geo, embedding)
  │
  ├─ Parallel ES recall (with simulated_at):
  │   ├─ Keyword recall (domain/keyword/geo matching + Gaussian decay)
  │   └─ kNN recall (if enabled, semantic similarity search)
  │
  ├─ Merge & deduplicate recall results
  │
  ├─ Timestamp filter: updated_at <= simulated_at
  │
  ├─ Ranker.Rank(candidates, profile, limit) with custom config + simulated_at
  │
  ├─ Group dedup: collapse by group_id (keep best score per group)
  │
  ├─ Relevance threshold: split into ranked vs filtered
  │
  ├─ Exploration slots (if configured)
  │
  ├─ Bloom filter dedup (if use_feed_history: true, read-only)
  │
  └─ Build response with full item content + score breakdowns + stats
```

## Directory Structure

```
replay/
├── main.go           # Hertz server on :8092, init ES client + DB pool
├── handler.go        # POST /api/v1/replay/sort handler, request/response types
├── pipeline.go       # Orchestrates recall → rank → dedup → threshold → exploration
├── config.go         # Request param parsing, merge with prod defaults
└── scripts/
    ├── build.sh      # go build -o build/replay ./replay/
    └── start.sh      # Start replay service
```

## Files to Create/Modify

| File | Action | Description |
|---|---|---|
| `replay/main.go` | Create | Hertz server, ES client + DB init, route registration |
| `replay/handler.go` | Create | Request/response types, handler function |
| `replay/pipeline.go` | Create | Sort pipeline orchestration (recall → rank → dedup → threshold) |
| `replay/config.go` | Create | Param parsing, merge logic with prod defaults |
| `replay/scripts/build.sh` | Create | Build script |
| `replay/scripts/start.sh` | Create | Start script |
| `rpc/sort/dal/es_query.go` | Modify | Accept `now` parameter for decay origin and expire filter (currently uses `time.Now()`) |
| `rpc/sort/dal/es.go` | Modify | Thread `now` parameter through search functions |
| `scripts/common/build.sh` | Modify | Add replay to build list |
| `scripts/local/start_local.sh` | Modify | Add replay service to local startup |
| `docs/dev/configuration.md` | Modify | Add replay service port (8092) |
| `docs/dev/testing.md` | Modify | Document replay module usage |

## Access Control

No application-level authentication. Access is restricted via network isolation at the deployment layer (same as console API on port 8090 — not exposed to public network).

## Non-Goals

- LLM evaluation logic — handled by the external evaluation service
- Caching of replay results — determinism is more valuable than speed for offline evaluation
- Batch replay (multiple scenarios in one request) — the external service handles orchestration
