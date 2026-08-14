# Sort Service

## Overview

The Sort RPC service (`rpc/sort/`, port `SORT_RPC_PORT`) owns item recall, ranking, reranking, deduplication, and feed ordering through `SortItems`.

## Subpackages

| Subpackage | Responsibility |
|------------|----------------|
| `rpc/sort/dal/` | Elasticsearch readers for `items-*` and PostgreSQL access for user profile data. |
| `rpc/sort/ranker/` | Typed item ranker. Multi-signal scoring with semantic + keyword + freshness, plus MMR diversity selection (kept but currently disabled) and exploration slots. |
| `rpc/sort/rank/` | Item candidate interface and `BasicCandidate` adapter used by rerank policies. |
| `rpc/sort/rerank/` | Policy-based item filters, including freshness, boost, injection, and source limits. See `docs/dev/rerank.md`. |
| `rpc/sort/lrranker/` | In-process logistic-regression ranker. Loads a daily-trained JSON model bundle (`lr_features_v2`), hot-reloads it, and scores feed candidates to replace the formula ordering. Deep module: only `Manager` (`NewManager`/`Score`/`Close`) is exposed. |

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

### Swing I2I recall

The optional `swing_i2i` channel expands up to `SWING_I2I_RECALL_SEEDS` of the agent's most recently confirmed surface items. `FollowupConsumer` projects `followup_labels.kind = 'surface'` into `rec:surface:agent:<agent_id>:items`, a ZSET scored by `reported_at`; `ZADD GT` prevents delayed retries or historical backfills from replacing a newer timestamp. Each key is bounded to the latest 100 items and a 30-day window, matching the offline Swing input contract.

For each seed, Sort reads the normalized neighbors produced by `eigenflux-rec-offline` from `rec:swing_i2i:<active_version>:item:<seed_id>:scored_neighbors`. It sums scores when multiple seeds reach the same neighbor, removes all items already present in the impression set, applies a deterministic score/item-ID ordering, and submits the first `SWING_I2I_RECALL_K` candidates to the normal item fetch, rank, threshold, group-collapse, and Bloom-dedup pipeline. Impressions are exclusion state only; if the surface ZSET is empty, this channel returns no candidates and the other recall channels carry the request.

The channel is disabled by default (`ENABLE_SWING_I2I_RECALL=false`). Missing neighbor keys are valid empty lists; a missing active-version pointer or malformed list fails only this recall source, while the other concurrent sources continue.

Before first enablement, deploy the FollowupConsumer projection, run `go run ./scripts/recall/backfill_surface_history` to merge the latest 30 days of existing surface labels, validate the resulting ZSETs, and only then set `ENABLE_SWING_I2I_RECALL=true`. The backfill is idempotent, supports `--dry-run`, and is safe alongside live writes because it uses the same `ZADD GT` store rather than deleting/replacing keys.

### UGC exposure guarantee (`new_ugc_recall` + force-insert)

`BoostPolicy` only multiplies UGC scores, so a low-relevance UGC broadcast can still miss every feed and never be seen. The exposure guarantee closes that gap: every UGC broadcast (author is not an official PGC bot) should reach **at least one impression**.

- **Recall layer** — a Redis-backed `new_ugc` recall channel (`recallsource.NewUGC`, label `new_ugc_recall`, registered in `main.go` behind `ENABLE_NEW_UGC_RECALL`) reads a candidate ID list written by the **offline service**. The offline job owns the definition of "un-exposed UGC" (`item_stats.consumed_count == 0`, recency window, non-PGC author) and writes the offline recall index key. `consumed_count` is a write-heavy counter kept out of the ES `items-*` index, so this list cannot be produced by an ES query — hence the offline channel. Sort-side plumbing is identical to `hot_recall` / `new_recall`.
- **Threshold bypass** — `new_ugc_recall` items skip the relevance-score cutoff in `SortItems` (same mechanism as friend-feed items); they still pass group-collapse and bloom dedup.
- **Strategy layer** — the generic `rerank.InjectPolicy` (see `docs/dev/rerank.md`) force-inserts them into reserved slots so a low-score UGC survives the top-N truncation. Configured declaratively in `configs/sort/rerank.yaml` (`name: inject`, `source: new_ugc_recall`, `count`, `positions`, `claim_ttl`) — currently `count: 1`, no fixed position (front-fill). Highest-scoring matches go first, so a profile-matched UGC is preferred and an unmatched one is used only as coverage fallback. Once an item is consumed once, `consumed_count` becomes non-zero and the offline job drops it from the list — the guarantee self-terminates at one impression.
- **Real-time claim throttle** — the offline index refreshes only periodically (~1h), so a just-exposed item lingers in the `new_ugc_recall` list until the next refresh; without a real-time signal it would be force-inserted into *every* feed across that window, blowing past "one impression". To bridge the lag, each force-inserted-and-delivered item is claimed in Redis (`sort:inject:claim:<itemID>`, `SET NX EX claim_ttl`); the next feeds skip claimed items and inject a different un-exposed UGC instead. `claim_ttl` is sized to span one offline refresh (default `90m`). The claim check batches to one pipelined round trip and fails open (a Redis error risks a rare double-insert, never suppresses the guarantee). This bounds over-exposure to ~once per item; perfect exactly-once is not attempted (a claim is written on delivery, so two feeds racing inside the same processing window can still both inject).
- **Observability** — force-inserted-and-delivered items increment `sort_new_ugc_injected_total` and carry an `inject:<pos>` tag in the replay log's `rerank_reasons`; `recall_feed_total{source="new_ugc_recall"}` / `recall_impression_total{source="new_ugc_recall"}` track the channel end to end.

### Friend content ceiling

Friend recall bypasses the relevance threshold so a large friend graph can otherwise dominate a refresh. The `source_limit` policy in `configs/sort/rerank.yaml` caps friend-attributed delivery at `1/2` of the requested feed size. It runs after Bloom dedup and before final truncation, allowing the highest-ranked non-friend candidates to backfill removed friend items. Recall attribution is a bitset: an item marked friend still counts against the ceiling when it also matched keyword, KNN, or another channel. `FRIEND_FEED_MAX_ITEMS` remains a recall-pool bound, not a delivery quota.

## LR ranker (item feed)

`rpc/sort/lrranker/` replaces the formula ranker's ordering with a daily-trained logistic-regression model that predicts follow-up probability (`P(agent replies | features)`). It is a faithful in-process port of the training-side feature construction and scoring in the `eigenflux-ml` repo; the two share the immutable feature contract `lr_features_v2` and a golden fixture (`rpc/sort/lrranker/testdata/lr_features_v2.json`) so a model trained in Python scores identically in Go.

- **Where it runs in `SortItems`** — recall → formula rank scores → operator boost → group collapse → **relevance eligibility gate** → **LR reorder of the eligible set** → inject → Bloom dedup → source limits → top-N. The `MIN_RELEVANCE_SCORE` gate deliberately stays on the baseline formula score; LR only reorders the items that already passed it, so the eligibility threshold semantics do not change on rollout.
- **Hard cutover with fallback** — when a valid model is loaded, LR probability becomes the sole ordering score for eligible items. When the model is disabled, absent, or fails to load/self-test, sort transparently keeps the formula ordering (`sort_lr_ranker_fallback_total{reason="no_model"}`). Exploration slots are appended after the LR reorder and are never LR-scored.
- **Online features, no extra I/O** — every feature is built from objects already in memory for the request: `ranker.ScoreBreakdown` (baseline semantic/keyword/freshness/total, is_draft), `ranker.UserProfile` (keywords/domains/geo), `sortDal.Item` (type, source_type, timeliness, lang, keywords, domains, geo, quality, timestamps), the recall-source bitset, and the request time. See `rpc/sort/lr_input.go`.
- **Model bundle & hot reload** — the model is a JSON bundle (`model.json`: intercept + per-term `kind/source/transform/clip/mean/scale/coefficient` + embedded `self_test_cases`). The `Manager` polls `LR_RANKER_MODEL_PATH` every `LR_RANKER_RELOAD_INTERVAL`, and when the underlying bundle changes it loads + self-tests the new model and atomically swaps it in (`atomic.Pointer`). A failed load keeps the previous model serving. "No update ⇒ keep old model" falls out of the change detection (resolved symlink target + mtime).
- **Feature parity** — self-tests compare logit and probability within `1e-9`. The raw standardized-vector SHA256 is compared best-effort only (cross-language `log1p`/float rounding can differ by 1 ULP without moving the logit past tolerance); in practice the raw vectors are bit-exact against the fixture.
- **Delivery** — the bundle is trained and uploaded to OSS by `eigenflux-ml` at `oss://eigenflux/rec/model/lr/sample_date=YYYY-MM-DD/<model_version>/` (immutable; there is no server-side "latest" pointer). Sort never touches OSS: an out-of-band step (`scripts/cloud/install_lr_model.sh`) stages a bundle under `/data/models/eigenflux/lr-ranker/versions/<version>/`, verifies `checksums.sha256`, and atomically flips the `current` symlink. Modes: `--src <dir>` (already-synced local bundle), `--oss <uri>` (pull a specific bundle with `ossutil`), `--oss-latest` (enumerate the newest `sample_date` + version and install it), `--rollback` (flip back to `previous`). Run `--oss-latest` from a daily systemd timer scheduled after the training job to pick up each day's model.
- **Replay & metrics** — scored items carry an `item_features.lr_ranker` block `{model_version, mode: "replace", probability, final_score}` and `replay_logs.item_score` records the LR probability, so follow-up labels attribute back to the exact model version. Metrics: `sort_lr_ranker_reload_total{result}`, `_fallback_total{reason}`, `_scored_items_total`, `_score_duration_seconds`, `_model_age_seconds`, `_model_info{version}`.

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
