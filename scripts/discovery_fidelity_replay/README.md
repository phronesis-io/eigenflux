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

For source-title/publication metadata changes after the summary auditor is already
live, use `FIDELITY_SOURCE_CONTEXT_ONLY=true FIDELITY_PGC_COUPLED=true` and run
`pgc_pipeline_replay -audit-both` for both bodies. This keeps the deployed PGC
summary audit enabled in both extraction arms and leaves every unchanged captured
summary alone. Reports use the `source-context` variant, keeping the prior auditor
rollout out of this change's estimated effect. Source-context mode also applies the re-extracted PGC expiry to both lexical
recall and authoritative hydration while preserving creation times and IDs.
Model re-extraction variability
remains part of the sensitivity; these estimates do not establish causal uplift.
`pgc_pipeline_replay -workers N` defaults to 8 and accepts 1–32 bounded workers.

## Fixed-cohort consumed-candidate replay

`-mode refill-capture` requires `cohort.json` containing exactly 225 distinct
`{"AgentID": <int64>}` rows chosen before comparing outcomes, plus frozen deployed
`rules.json`, `model.json` and `rerank.yaml`. It compiles current contexts for every
Agent, including Agents absent from recent delivery logs, reads all enabled
recall channels, and captures up to 640 additional lexical hits per context.
It copies live forward hashes into isolated local Redis before authoritative
hydration; repairs write only there. Shared item forward facts and injection
claims are frozen on first observation per capture process (bounded at 100,000
items); owner-specific authority and consumption are read for each Agent. Capture workers have separate scratch Redis databases (six by default;
`REFILL_CAPTURE_WORKERS=1..12`), at most six ES/remote-Redis connections and
four read-only PG connections.
Failures remain errors and are retried by a later invocation; they are not zeros.

For a slow SSH tunnel, `REFILL_CAPTURE_SSH_HOST=<read-host>` streams production
query bodies through six bounded, read-only SSH workers. They execute only item
searches and read-only consumption scripts, and calculate the same content
fingerprint near the data; full source
text crosses the connection only for exclusion filters. A cross-language parity
test covers Unicode, HTML escaping, slot ordering and IDs above 2^53.
Consumption parity tests cover owner isolation, the 30-day boundary and chunking. This mode
requires the read host's `/etc/eigenflux/runtime.env` and Python standard library;
it creates no remote files and sends no delivery/feedback requests. The normal
local capture transport narrows friend recall to IDs because that channel only
consumes IDs. `REFILL_CAPTURE_VERBOSE=1` logs query sizes and timings, not values.

`-mode refill-replay` runs the production Engine, deployed rules/LR/rerank,
200-candidate Feed prefetch and page source ceilings for 24 repeated refresh
requests of 20 slots per Agent. Each arm independently advances consumption and
shared cross-Agent injection claims. It verifies A/A equality from the same
captured inputs and rejects duplicate/initially consumed delivery. This is a
frozen-supply exhaustion scenario, not a reconstruction of a historical day:
time, contexts and supply stay fixed; claims do not expire; no new content,
context updates, request-frequency differences or cache `load_more` schedule are
invented. A/A checks snapshot determinism, not historical causality.

A truncated capture is flagged when fewer than 80 refill rows remain or the
bounded history prefix drops an initially excluded group. Such arms are censored
and cannot establish the release gate. The 8,192-group production history bound
and 750 ms optional budget stay active during replay; online dependency latency
must be measured separately because frozen sources are local.

`refill_report.py --dir <private-dir> --out <sanitized-dir>` emits anonymous
per-Agent CSV rows, request-by-request source mix, rejection stages, original-item
retention, friend provenance independent of winning context, and two
positive-event sensitivities. The enabled serving arm uses empty-page recovery;
nonempty and partial pages keep the baseline snapshot. It requires `rate-history.jsonl` with fixed-window
Agent/source/age delivery and positive-event counts. Both estimates use a
20-delivery cohort prior: source-only and source-plus-age. Missing exposure
support is an error; unlinked feedback is not classified as negative. Event lag,
missing impression linkage and exposure selection prevent interpreting these
as calibrated positive probabilities or causal daily uplift. Keep credentials,
cohort IDs, queries and item snapshots out of the repository and PR.

## UGC publisher ownership

`FIDELITY_UGC_OWNERSHIP=true -mode compare` keeps PGC unchanged and replaces UGC
summary text with the exact decoded author `notes.summary`, or empty text when
absent. Invalid author formats are excluded as publication-time rejections.
This is a prospective steady-state sensitivity, not a historical rewrite. Body
embeddings remain unchanged because the worker embeds raw content. The same
complete-cohort and unchanged-content A/A requirements apply.

`-mode ownership-evaluate` incrementally evaluates potentially eligible captured
UGC with the configured production Slop model and omitted-summary prompt. Cache
identity binds source, prompt, model, request contract, date and token limit.
`FIDELITY_UGC_METADATA=true` additionally applies admission, quality, source type,
language, keywords and expiry. Provider failures, missing evaluations and type
changes requiring a grouping replay stop comparison; they never count as passes.
Use `report.py --variant ugc-ownership` or `ugc-ownership-metadata` for the
corresponding private per-Agent delivery/expected-positive reports. Anonymize
Agent identifiers before publishing evidence.

For bounded on-host read-only diagnostics, `FIDELITY_IN_PROCESS_SCRATCH=true`
uses a disposable loopback Miniredis process-local cache for history pruning and
feature repair. It never substitutes production reads; the separate guarded
production Redis client still supplies authoritative facts. Treat this cache as
a diagnostic implementation and require the same exact full-cohort A/A gate.
No application service, production source tree or persistent Redis is changed.

Positive-effect estimation requires `unique-impression-positive-events-v2` history:
exact unique impression/Agent/item matches, no self-deliveries, and no ambiguous
or missing impression IDs in the fitted denominator. Observed unlinked feedback
is reported separately and cannot inflate expected positives. Re-export old
history caches; no unsupported source prior is silently treated as zero. Paired
UGC prompt evaluation retains both current-model extraction arms under the same
source/date/contract, distinct from comparison against historical stored metadata.

Metadata sensitivity includes currently language-filtered or expired UGC when
other immutable eligibility checks allow it, because those two fields can change
with extraction. `FIDELITY_MODEL_WORKERS=1..16` bounds offline model concurrency
(default 4). For changed types, run `ownership_neighbors.py --dir <private-dir>`
using on-host read-only ES access. It verifies unchanged source text, reuses the
stored body vector and applies production type-specific grouping against preceding
retained items. Comparison requires source/type-bound successful grouping evidence;
missing vectors, source drift or query failures remain release blockers. Historical
neighbor deletion and later group changes limit this to sensitivity evidence.

`FIDELITY_UGC_PAIRED=true` together with ownership/metadata flags adds a separate
`ugc-ownership-paired` sensitivity. Its before arm re-extracts valid-format UGC
with the current model's legacy prompt, using the already saved paired results;
the after arm uses the omitted-summary prompt. Invalid author formats retain
captured before metadata and are rejected after. Run the neighbor reader with
`--arm baseline` as well. Report this alongside captured-before comparisons to
expose historical model/date drift; stochastic paired runs cannot establish
causality. This mode cannot replace the unchanged-corpus A/A gate.
