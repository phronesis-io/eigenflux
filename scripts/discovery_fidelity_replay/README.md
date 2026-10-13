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
`compare` only permits `FIDELITY_LOCAL_ES_URL=http://127.0.0.1:56921` for writes.

Summary audits bind the source content, notes and prior summary by SHA-256 and
also bind the current audit prompt. Every potentially eligible candidate must
have an audit; seen or immutable-hard-filter failures remain in the pool but need
no model call. Full-pool source export reuses only fingerprint-matching records.

The counterfactual reindexes the full captured union with production analyzers.
Changed PGC body arms include fresh extraction and embedding calls; unchanged
embeddings and other recall channels retain their captured evidence. Changed
vectors can enter dense top-k from elsewhere in the full captured pool. Item IDs,
lifecycle dates stay fixed. PGC group assignments are recomputed from preceding
production items using the same cosine thresholds and type rules. Failed model arms are excluded,
not counted as unchanged successes. PGC primary-model replay is a sensitivity
comparison; it does not model every production salvage/fallback branch.

Limits: finite-corpus BM25 statistics differ from production, and items outside
all original recall channels cannot enter this pool. The grouping sensitivity query covers the current retained production corpus;
previously deleted documents and original pre-publication cache state cannot be recovered. These limits must accompany results;
exact baseline reproduction does not make the counterfactual globally exact.

`python3 scripts/discovery_fidelity_replay/report.py --dir <private-dir>` produces
one CSV row per Agent, source/Need/Profile delivery mix and expected positive
events. The latter uses the fixed cohort's real feedback rate by Agent/source,
with a 20-delivery source prior. It assumes no factuality uplift and is not the LR
follow-up score. Feedback lag, missing impression linkage and sparse histories
limit the estimate; compare next-day observed daily positives after deployment.
