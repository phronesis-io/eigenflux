# Sort Service

## Overview

The Sort RPC service (`rpc/sort/`, port `SORT_RPC_PORT`) owns every read-side discovery path: item feed ordering (`SortItems`), service search (`SearchServices`), and the cross-type rerank infrastructure (`rpc/sort/rank` + `rpc/sort/rerank`) used to mix typed candidates when a surface needs items and services together.

Trade owns the write side of services; feed owns impression / delivery. Sort sits between them.

## Subpackages

| Subpackage | Responsibility |
|------------|----------------|
| `rpc/sort/dal/` | Elasticsearch readers for `items-*` (item recall) and `services-*` (service recall: `SearchServices`, `SearchServicesByEmbedding`, `FetchServiceByID`, `ServiceDoc`). PostgreSQL access for user profile data. |
| `rpc/sort/ranker/` | Typed item ranker. Multi-signal scoring with semantic + keyword + freshness, plus MMR diversity selection (kept but currently disabled) and exploration slots. |
| `rpc/sort/serviceranker/` | Typed service ranker. 6-signal weighted scoring: semantic, keyword (BM25 passthrough), success_rate, latency (inverse), price (inverse), deadline (inverse). |
| `rpc/sort/rank/` | Cross-type `Candidate` interface and `BasicCandidate` adapter, used when items and services need to flow through the same rerank policy. |
| `rpc/sort/rerank/` | Policy-based mixer and filters: `FreshnessPolicy`, `DedupPolicy`, `NormalizePolicy`, `BoundsPolicy`, `RatioPolicy`, `SlotPolicy`. See `docs/dev/rerank.md` for the full description. |

### Item Timeliness

Sort applies configurable item rerank policies from `configs/sort/rerank.yaml` after recall and before item ranking/exploration:

- `alert` is hard-limited by age. The default YAML rule drops alerts older than `6h` because stale urgent information is worse than silence.
- Within the allowed alert window, the existing decay still applies: `FRESHNESS_ALERT_OFFSET=2h`, `FRESHNESS_ALERT_SCALE=12h`, `FRESHNESS_ALERT_DECAY=0.5`.
- `demand` uses `expire_time` for urgency-aware freshness and drops to zero freshness after expiry.
- `info` and `supply` remain score-decayed only, with `supply` using the slower supply-specific curve.

The Sort service reads this YAML once during startup. If the file is missing or invalid, Sort logs a warning and runs without configured item rerank policies.

```yaml
policies:
  - name: freshness
    item_rules:
      - broadcast_type: alert
        max_age: 6h
        action: drop
```

## SearchServices

`SearchServices` is the open-ended task → services discovery RPC. It is exposed over HTTP as `POST /api/v1/trading/services/search` (gateway handler `api/handler_gen/eigenflux/api/api_service.go` `SearchTradingServices` → `SortClient.SearchServices`). Internally it owns the multi-intent recall + cross-intent rerank pipeline; the buyer agent submits an unstructured task description plus an optional pre-decomposed `sub_intents` list, and sort returns one flat ranked list of services with each result tagged by which sub-intent it matched.

### Request

```json
{
  "raw_query": "翻译西语原始材料生成PPT做presentation",
  "sub_intents": [
    {"name": "翻译",     "query_text": "西班牙语→中文翻译",        "importance": 1.0},
    {"name": "PPT生成", "query_text": "根据要点生成 PPT",          "importance": 0.9},
    {"name": "领域解读", "query_text": "对原始材料做专家级解读",     "importance": 0.7}
  ],
  "limit": 30,
  "filters": { "max_price_atomic": 1000000, "deadline_ms_max": 3600000 }
}
```

Rules: `raw_query` required (also stored in the replay log as truth of record). `sub_intents` optional and capped at 8 after dedup; absent → server LLM decomposes via `pipeline/llm.DecomposeTask`. `importance` defaults to 1.0 when omitted. `limit` default 30, max 100.

### Sub-intent resolution

| `debug.sub_intents_source` | When |
|---|---|
| `agent` | Agent supplied `sub_intents` |
| `llm_fallback` | Agent omitted; server called `DecomposeTask` |
| `single_intent_fallback` | Agent omitted AND LLM unavailable/failed; the raw query becomes one synthetic intent |

### Pipeline

1. Resolve sub-intents (per above).
2. Embed each sub-intent's `query_text` in parallel via the sort-side `embeddingClient`.
3. Fan out per-intent recall via `dal.SearchServicesByIntents`: each lane does kNN against `usage_embedding` plus BM25 over `title^2 + capability_desc + call_spec_text + use_cases + keywords`, filtered by `status=active` and the request-level `filters`. Per-intent recall cap defaults to 50 (env `TASK_MATCH_RECALL_SIZE_PER_INTENT`).
4. Join `trading_service_stats` (`BatchGetServiceStats`).
5. For each match, score against every intent that recalled it with `serviceranker`. Aggregate = `max(perIntent[name] * importance[name])`. `winning_intent = argmax`. `matched_intents` is the set union.
6. Rerank chain: `DedupPolicy → NormalizePolicy{MinMax} → CoveragePolicy → BoundsPolicy{Ceiling}`. CoveragePolicy guarantees each protected intent (importance ≥ 0.5) appears at least once in the top-`limit` window. BoundsPolicy caps any single type at 10 within the window.
7. Build response + emit replay envelope.

### 6-signal service ranker

Each candidate gets a `serviceranker` score combining:

```
score = semantic     * 0.55
      + keyword      * 0.15
      + success_rate * 0.15
      + (1 − min(avg_latency_ms     / MAX_LATENCY,  1)) * 0.07
      + (1 − min(amount_atomic      / MAX_PRICE,    1)) * 0.05
      + (1 − min(delivery_deadline_ms / MAX_DEADLINE, 1)) * 0.03
```

Weights are configurable via environment variables (defaults):

| Variable | Default | Meaning |
|----------|---------|---------|
| `TRADE_SEARCH_SEMANTIC_WEIGHT` | `0.55` | Cosine similarity to query embedding |
| `TRADE_SEARCH_KEYWORD_WEIGHT` | `0.15` | BM25 keyword match score |
| `TRADE_SEARCH_SUCCESS_WEIGHT` | `0.15` | Seller `success_rate` |
| `TRADE_SEARCH_LATENCY_WEIGHT` | `0.07` | Inverse of `avg_latency_ms` |
| `TRADE_SEARCH_PRICE_WEIGHT` | `0.05` | Inverse price signal |
| `TRADE_SEARCH_DEADLINE_WEIGHT` | `0.03` | Inverse `delivery_deadline_ms` |

The `TRADE_SEARCH_` prefix is retained from a prior ownership location to avoid churn in deployment configs; the variables now feed sort's `serviceranker` at startup.

### Response stats block

The per-result `stats` map in the RPC response carries four rolling counters joined from `trading_service_stats`: `success_rate` (0-1 fraction), `avg_latency_ms` (number), `order_count` and `released_count` (integer counts).

The replay log's `item_features.stats` adds `refunded_count` and `expired_count` on top of those four. `last_activity_at` is fetched from `trading_service_stats` but is currently consumed only as a ranking signal — it is not propagated into the replay payload.

### Why rank signals live in PostgreSQL, not ES

`success_rate`, `avg_latency_ms`, `order_count`, `released_count`, `refunded_count`, `expired_count`, and `last_activity_at` change on every order completion. Replicating those writes into ES would force a full document re-index — including the embedding — per order event, making ES write traffic track order throughput rather than service publishing throughput.

Instead the counters live only in `trading_service_stats` (trade-owned). SearchServices joins them in after the ES recall via `tradedal.BatchGetServiceStats` over the shared `db.DB` handle. ES write traffic therefore tracks `PublishService` / `UpdateService` only.

### Cross-Service DB Access

Sort reads `trading_service_stats` from PostgreSQL directly via the trade DAL (`import tradedal "eigenflux_server/rpc/trade/dal"`). The shared `pkg/db.DB` handle is available because every RPC service initialises it from the same DSN. No new RPC method on trade is required — adding one would only add latency and IDL churn for a query that is already a single indexed SELECT.

### Failure modes (silent degradation)

| Failure | Behavior |
|---|---|
| LLM decompose call fails | `single_intent_fallback`; warning logged; `sort_search_services_llm_fallback_total{reason="llm_error"}++` |
| Sub-intent embedding fails | That lane runs BM25-only |
| One ES lane fails | Skip that intent; other intents continue |
| All lanes fail with zero candidates | Return empty results (not an error) |
| Stats fetch fails | Score with zero stats; degrades gracefully |

### Metrics (pkg/metrics)

`sort_search_services_requests_total{sub_intents_source}`, `sort_search_services_sub_intents` (histogram), `sort_search_services_llm_fallback_total{reason}`, `sort_search_services_latency_ms{phase=resolve|embed|recall|rerank|total}`, `sort_search_services_empty_total`.

`SortItems` category-distribution counters, both labelled `{broadcast_type, source_type}` (empty values collapse to `none`):

- `sort_recall_category_total` — recall candidates before ranking/boost/dedup.
- `sort_feed_category_total` — items actually delivered to the feed after boost and dedup.

Comparing feed-share vs recall-share per category quantifies the `BoostPolicy` effect (supply/demand ×1.3, UGC `source_type=original` ×1.2). Charted in the "召回 vs 下发品类分布 / Boost 监测" row of the `ugc-content` Grafana dashboard.

### Replay log

Each SearchServices request emits one envelope to `stream:replay:log` with `impression_id = imp_search_<unix-nano>`, `agent_id = 0`. `agent_features` carries `surface = "search_services"`, `raw_query`, `sub_intents_source`, `effective_sub_intents`, `filters`. Per-item `item_features` adds `entry_type = "service"`, `matched_intents`, `per_intent_score`, `winning_intent`, `normalized_score`, `rerank_reasons`, the 6-signal `rank_scores`, and the joined `stats` block. `rerank_reasons` deliberately excludes `coverage:*` tags to keep replay-log cardinality bounded.

## SortItems — Mixed Items + Services

`SortItems` is the canonical read entry point for the feed. By default it returns items only (legacy behaviour). When `ENABLE_SERVICE_MIX=true`, the handler additionally recalls trading services using the same profile-derived keywords and domains, ranks them with `serviceranker`, and merges the two streams through the rerank chain:

```
DedupPolicy
  → NormalizePolicy{Method: MinMax}        // put item and service scores on [0, 1] per type
  → BoundsPolicy{
        Limit: limit,                         // == req.Limit
        Bounds: {service: {Floor: 1}},        // guarantee ≥1 service in the window
    }
```

`BoundsPolicy.Floor:1` together with `Limit = req.Limit` causes the lowest-scoring tail item inside the window to be tail-replaced by the highest-scoring service when the natural score order produces zero services. When the recall returns no services at all the floor degrades silently — feeds without active services remain item-only.

The response shape changes only in `sorted_items`: each `SortedItem` carries an `entry_type` field set to `"item"` or `"service"`. The legacy `item_ids` field widens to "all ranked IDs in order", regardless of type; consumers that only care about items should consult `sorted_items[i].entry_type` to disambiguate.

### Replay log schema for mixed entries

Every `SortedItem` emitted by `SortItems` carries `item_features` populated for the replay log:

- **Items** — original item-stage features (`broadcast_type`, `domains`, `rank_scores`, `recall_source*`, …) are preserved, then augmented with `entry_type:"item"`, `normalized_score` (the post-`NormalizePolicy` score that drove ordering), and `rerank_reasons` (tags such as `normalize:minmax`, `bounds:displaced`) when the candidate was touched by the mixer.
- **Services** — built fresh as `entry_type:"service"`, `service_id`, `seller_agent_id`, business fields (`title`, `capability_desc`, `domains`, `amount_atomic`, `asset`, `delivery_deadline_ms`, `updated_at`), the 6-signal `rank_scores` breakdown from `serviceranker`, a `stats` block (`success_rate`, `avg_latency_ms`, `order_count`, `released_count`, `refunded_count`, `expired_count`), `recall_source_names:["service_es"]`, `normalized_score`, and `rerank_reasons`.

Both shapes share the keys downstream replay analysis already keys off (`entry_type`, `rank_scores`, `recall_source*`, `normalized_score`, `rerank_reasons`), so mix decisions and per-signal contributions are uniformly inspectable across kinds.

### Request-scoped context features

The `agent_features` block stamped onto every `SortedItem` is request-scoped (feed extracts it once per impression and stamps it onto the replay log). It carries the agent's profile signals (`keywords`, `domains`, `geo`) plus a nested `context` object projected from the client headers extracted by the gateway's `ClientInfoMiddleware` and propagated via Kitex metainfo (`pkg/reqinfo`):

```json
{
  "keywords": [...],
  "domains":  [...],
  "geo":      "...",
  "context": {
    "client_host":    "openclaw/0.0.12",
    "client_channel": "openclaw",
    "client_id":      "ab12cd34",
    "client_os":      "darwin/arm64",
    "client_tz":      "Asia/Shanghai",
    "client_lang":    "zh-CN",
    "cli_ver":        "0.0.7",
    "cli_ver_num":    7,
    "skill_ver":      "1.2.3",
    "skill_ver_num":  10203
  }
}
```

Empty fields are omitted so requests without client headers (internal calls, dev) don't carry a stub `context` block. Version numbers are emitted as ints alongside their string form for numeric comparison downstream. Source headers stamped at the HTTP boundary: `X-Client-Host`, `X-Client-Channel`, `X-Client-ID`, `X-Client-OS`, `X-Client-TZ`, `X-Client-Lang`, `X-CLI-Ver`, `X-Skill-Ver`.

### Config

| Variable | Default | Meaning |
|----------|---------|---------|
| `ENABLE_SERVICE_MIX` | `false` | Off — `SortItems` returns items only. On — service recall + rerank kicks in. |
| `SERVICE_MIX_RECALL_SIZE` | `50` | Cap on services pulled from `services-*` per request before rerank. |

### Failure modes

- ES service recall errors → log + fall back to items-only response (no error to caller).
- Stats fetch errors → rank services with zero stats; degrades gracefully.
- Empty recall (no services match) → response is items-only and identical to the flag-off path.

The mix is purely additive within the existing `SortItems` contract; callers that do not yet handle `entry_type` continue to function — they just see service IDs they cannot look up via item endpoints. Feed-side wiring (extending `FeedItem` to render service entries) ships in a follow-up PR.
