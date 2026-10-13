# Summary and attribution replay

This CLI captures all production recall channels for a fixed recent Agent cohort,
including unsuccessful candidates and empty results. It runs the production
Discovery engine, rules, LR model and source policies with frozen contexts,
hydration, seen facts, source classes and injection claims. `-mode replay` requires
an exact baseline match for every Agent and rejects incomplete captures.

Production PostgreSQL sessions must enforce read-only mode. The Redis command
hook rejects writes. Surface-history pruning and feature-cache readthrough repairs
use an isolated local Redis. Model calls do not publish or mutate production data.
Keep source snapshots, vectors, queries and credentials outside the repository.

Modes: `capture`, `evidence`, `history`, `observed`, `replay`, `compare`.
Supply `-dir` with private frozen `rules.json`, `rerank.yaml`, `model.json`, and
`index-template.json`. Capture requires a read-only PostgreSQL DSN, local scratch
`REDIS_ADDR`, `FROZEN_REMOTE_REDIS_ADDR` and a read-only production ES connection.
Run `FIDELITY_BASELINE_ONLY=true` for the unchanged-content A/A gate. Each run
indexes original/off/on variants under filtered aliases sharing the same corpus
statistics. Set
`FIDELITY_PGC_COUPLED=true` for the combined sensitivity.
`compare` only permits `FIDELITY_LOCAL_ES_URL=http://127.0.0.1:56921` for writes.

The final counterfactual applies summary corrections only to the frozen PGC
class. UGC keeps its original content and summary, even if a diagnostic audit
exists. Coupled source arms must belong to PGC.

Summary audits bind the source content, notes and prior summary by SHA-256 and
also bind the current audit prompt. Every potentially eligible candidate must
have an audit; seen or immutable-hard-filter failures remain in the pool but need
no model call. Full-pool source export reuses only fingerprint-matching records.

The counterfactual reindexes the full captured union with production analyzers.
Production lexical scores remain the anchor. For changed text, local before/after
score ratios estimate the delta under identical field statistics. A median
production/local calibration estimates new lexical matches already present
elsewhere in the full pool. Off-channel candidates must cross the original
production cutoff; their estimated baseline is capped by that cutoff. Unchanged scores
and unrelated stale-version rejections are preserved. This avoids treating the
small index's different IDF statistics as a release benefit. The unchanged-content
A/A run must reproduce all captured ordered results exactly.
Changed PGC body arms include fresh extraction and embedding calls; unchanged
embeddings and other recall channels retain their captured evidence. Changed
vectors can enter dense top-k from elsewhere in the full captured pool. Item IDs,
lifecycle dates stay fixed. PGC group assignments are recomputed from preceding
production items using the same cosine thresholds and type rules. Failed model arms are excluded,
not counted as unchanged successes. PGC primary-model replay is a sensitivity
comparison; it does not model every production salvage/fallback branch.

Limits: lexical deltas remain estimates because finite-corpus BM25 term weights
differ from production; unchanged off-channel lexical refill is conservative. Items outside
all original recall channels cannot enter this pool. The grouping sensitivity query covers the current retained production corpus;
previously deleted documents and original pre-publication cache state cannot be recovered. These limits must accompany results;
exact baseline reproduction does not make the counterfactual globally exact.

`python3 scripts/discovery_fidelity_replay/report.py --dir <private-dir>` produces
one CSV row per Agent, source/Need/Profile delivery mix and expected positive
events. The latter uses the fixed cohort's real feedback rate by Agent/source,
with a 20-delivery source prior. It assumes no factuality uplift and is not the LR
follow-up score. Feedback lag, missing impression linkage and sparse histories
limit the estimate; compare next-day observed daily positives after deployment.

`neighbors.py --dir <private-dir>` reads preceding retained production items for
both PGC embedding arms. It uses the production float32 0.80 cosine cutoff and
type-specific grouping rules, with no writes to ES. Source credentials come from
explicit environment variables. A historical item cannot match itself or an item
published later. These queries cannot recover already deleted historical items.
