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

## New PGC supply

`FIDELITY_CURRENT_SERVING=true -mode capture` freezes the active Need-budget,
semantic, rating-LR and consumed-refill branches in each snapshot, including
profile vectors, cosine presence, consumption and optional recall results.
Supply the deployed `rating-model.json` as well as the original model/policies.
Capture uses at most two Agent workers. Forward projections are copied from
production before local-only read-through repair. A missing author, cosine,
model or recall fact invalidates acceptance rather than becoming zero.
The rating-count challenger is rejected until its evidence is supported.

For slow links, `REFILL_CAPTURE_SSH_HOST=<read-host>` also applies to this mode.
The existing bounded read-only workers execute search and consumption queries
near the stores. Full text is retained privately for new-supply lexical replay.
Offline cosine collection uses a 30-second read budget within a ten-minute
Agent capture deadline, independent of the serving 300 ms optional-stage budget.
Whole-union hydration uses the Agent deadline, with bounded individual store
reads; bulk Redis transfer has a 120-second transport timeout. Completed source
export batches are saved atomically and resume only after fingerprint validation.
Frozen execution retains serving deadlines. This is a healthy-dependency
counterfactual, not a test or estimate of production latency/fallback frequency.

Prepare `new-supply-unverified.json` with 1–50 real rescued candidates: private
synthetic IDs, source content/notes, current server extraction and embeddings,
explicit assumed creation times, retained-corpus group assignments and hashed
provenance. `pgc_pipeline_replay -audit-both -publisher-summary` matches the
publisher-notes summary route when that route is deployed. Source publisher
classification must be established separately; inputs are explicitly PGC.

`-mode supply-evidence` checks ID collisions, exact-content duplication, quality,
PGC author visibility, both-direction relations and each cohort owner's consumed
groups. It writes admitted `new-supply.json` and explicit rejections. It never
publishes, records impressions or changes production stores. Every owner needs
an explicit fact, including false values. Keep the provenance in
`new-supply-provenance.json`; never invent an online item receipt.

Run `FIDELITY_NEW_SUPPLY=true -mode compare` against isolated loopback ES. This
mode cannot be combined with summary/body interventions. It requires current
serving snapshots and exact original A/A reproduction. The baseline retains
all original PGC/UGC; only the treatment admits new sources to lexical, Need
dense and profile dense competition. It executes the 200-candidate Feed
prefetch and normal 20-slot page source ceilings. Baseline page reconstruction
must also equal the unchanged frozen source. Group consumption, exclusions,
expiry and source policies remain active. A treatment refill without its exact
captured query fails acceptance instead of assuming an unobserved deeper pool.

New lexical scores use the median production/local BM25 scale; an uncalibrated
new match is an error. Dense additions use exact cosine against the captured
production kNN top-k, which is still an approximation to rerunning production
ANN. No new-item promotion is forced. The manifest binds the cohort, snapshots,
models, rules, policies, source evidence, feedback history, index mapping, new
supply and provenance by SHA-256. Empty-admission execution must also reproduce
the baseline. Duplicate recall rows and captured tie order remain intact; the
replay does not repair production index-version conflicts.
`report.py --variant new-supply` adds admitted/displaced counts and uses matched
historical positive events with a 20-delivery source prior. Report each Agent's
PGC/UGC, Need/Profile, delivery and expected-positive changes. The workload is
one frozen refresh, not a simulated historical day; empirical rates are neither
calibrated probabilities nor causal uplift. Preserve these limits with results.

Run a second comparison with `FIDELITY_NEW_SUPPLY_NEW_RECALL=true` and report
`--variant new-supply-refreshed`. This explicitly admits the rescued items into
the next hourly new-PGC exposure queue before its top-k cutoff, displacing the
last original queue entries. It still applies normal hydration, ranking and
page ceilings. The first scenario leaves the offline queue frozen. These are
conditional membership sensitivities, not a reconstruction of the hourly job,
a mathematical bound or guaranteed exposure. Newly published items have no
interaction history to enter hot or item-to-item queues initially.
