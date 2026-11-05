# New-supply replay validation

## Release boundary

PGC PR #474 (`9f7f966c55ee0f6da4fc85f5132898f6b2196ec5`) remains draft.
This server change is offline tooling, not a serving rollout. Both PGC feature
switches remain disabled pending a complete fixed-cohort delivery comparison.
Querit remains retired. No new production item, impression, feedback, queue entry
or retry was created during this investigation.

The server baseline is `0919be90`, with the deployed server at `a044bff4`.
There are no differences between those commits in the serving sort,
configuration or LLM extraction packages used by this replay. The frozen
follow-up model is `lr_20261104_2302_b2189a73`; the enabled rating model is
`rating_lr_20261019_0000_offline`. Profile semantics, rating LR and consumed
refill are enabled for all Agents; the Need budget experiment is at 50%.
The rating-count challenger is disabled and is explicitly unsupported here.

## Supply and quality evidence

Two real discarded articles are the intervention: the Ming Pao article rejected
by the primary provider and then classified as an archive, and the Home Assistant
entity-renaming article classified as a stub. An isolated PGC replay applies the
exact observed model responses through the complete processor. Both are discarded
with review off and accepted with review on, including ordinary body/summary
validation. The outbound notes pass through the publisher's actual sanitization.
Both then pass current server extraction with publisher summaries and produce
1,024-dimensional embeddings. Neither is an exact-content duplicate or below the
production quality floor; resulting quality values are 0.70 and 0.75.

Synthetic IDs distinguish these unpublished candidates from real item receipts.
Publication is assumed 60 seconds after each original PGC indexing time. Grouping
uses preceding retained production items and the production float32 cosine/type
rules; both form new groups. Deleted historical documents and original cache
state cannot be reconstructed. Publisher identity uses a stable selection from
the real publisher pool; actual publishing selects an account at runtime. All 100 publisher accounts
are active and none has a contact/block relation with this cohort, so those
authority fields do not vary with the selected publisher in this sample.

The earlier quality investigation includes 12 cases and 21 model calls, with
174,613 reported tokens. Sports and archive controls remain rejected. Three
baseline reruns already accepted their articles and are not counted as rescued
supply. The two rescued articles do not establish population-wide model accuracy.

## Delivery evidence requirements

The fixed cohort contains 50 recently active Agents selected before outcome
inspection. Failed snapshots were retried without changing cohort membership; all 50
complete snapshots now pass exact ordered baseline reproduction.
Private evidence includes full original candidate pools, current real contexts,
consumption, authority, cosine availability and the actual forward projections.

The unchanged replay must exactly reproduce every ordered baseline execution.
Hydration inputs are compared as complete document multisets because the
production refill builds them from a Go map; returned documents follow current
input order. Changes in any candidate field still fail the check. An empty-admission replay
also has to reproduce the original Feed page. Dense merging preserves duplicate
index records and captured tie order, so offline tooling cannot heal a stale
production-version rejection and misreport that as intervention benefit. A separate
A/A check verifies the 200-candidate Feed prefetch and 20-slot page assembly.

Run two conditional treatment scenarios: the offline PGC exposure queue remains
unchanged, or it admits both new items before its top-k cutoff. This is a queue
membership sensitivity, not a reconstruction of the hourly exposure job or a
mathematical bound. Local lexical new-match scores are calibrated estimates;
exact cosine additions compete with captured approximate-nearest-neighbor pools.
Neither case assumes quality-driven positive-feedback uplift.

The report must include every Agent's PGC/UGC, Need/Profile (friend and baseline
remain separate), total delivery, new/displaced items and expected positive-event
counts. Positive estimates use matched historical impressions and feedback from
seven complete days, with at least one day of feedback maturity and a 20-delivery
source prior. The selected historical window contains 38,963 deliveries; a
separate read-only linkage check found no ambiguous/missing impression IDs and
no missing source-author records. Missing cohort exposure support is an error. One frozen refresh
is not a historical day or a forecast of daily request volume.

## Checks

Targeted Go tests with the race detector and `go vet` pass for both offline CLIs.
Python report tests cover actual source rates, sparse histories, displaced UGC,
matched new-supply feedback and missing-exposure rejection. Existing Discovery,
LR, legacy rerank and LLM package tests pass. The standard build script compiles
sort and pipeline. Integration acceptance runs the real engine against isolated
local Elasticsearch/Redis and frozen production evidence.

Before marking either repair ready, record the complete integration outcome and
per-Agent delivery report here. Before enabling PGC review, record the rollout
time and the following day's same-cohort daily-positive review date. Disabling
`PGC_DISCARD_RECHECK` stops future reviews without a code rollback or requeue.
Do not report next-day validation before those observations exist.
