# Consumed-candidate refill: fixed-cohort evaluation

Status: **draft; not approved for activation**. This report separates observed
historical counts from a paired, current-context supply-exhaustion replay.
The experiment switch defaults to zero. No production delivery, feedback,
configuration, or application code was changed for this investigation.

## Results and decision

**Use empty-page recovery only; keep the PR draft and the bucket at zero.**
A nonempty current page, even a partial page, retains the original snapshot and
never calls optional history/refill dependencies. An undersized 200-row prefetch
pool alone does not trigger recovery. Within a recovery wave, existing Need
precedence, LR/rerank, deduplication and source ceilings still apply.

All 225 Agents completed both 24-request arms, all 225 A/A checks match, and no
duplicate or initially consumed delivery was accepted. Capture ran from
2026-10-14 18:35:47 to 19:08:41 (UTC+8). The final code was replayed against these
same frozen inputs. Results and hashes are in
[fixed-225-summary.json](fixed-225-summary.json); the
[225-row CSV](fixed-225-effects.csv) preserves silent Agents, per-Agent delivery
mix, age, positive sensitivities, item retention and coverage status.

**Only 187/225 paired results have complete candidate coverage.** All 38 censored
treatment arms have `history_prefix_changed`: advancing consumption changes the
bounded 8,192-group prefix, so an initially excluded group can re-enter the actual
ES query. Its previously hidden candidates were not captured. This is a replay
coverage limit, not 38 failed requests or zero outcomes. No arm exhausted the
640-row captured refill depth. Missing outcomes must not be invented or counted
as a release pass.

For the **187 complete pairs**, over 24 requests per Agent:

| Metric | Off | Empty-page recovery |
|---|---:|---:|
| Delivered items | 6,226 | 86,598 |
| Original delivered items retained | 6,226 | 6,226 |
| Items with friend-source provenance | 673 | 673 |
| Winning Friend-context deliveries | 672 | 672 |
| Zero-delivery Agents | 43 | 0 |
| Empty requests (of 4,488) | 4,069 | 2 |
| PGC / UGC share | 54.42% / 45.58% | 45.60% / 54.40% |
| Need / Profile share | 10.31% / 78.89% | 25.46% / 73.76% |
| Content > 30 days | 49.86% | 51.03% |

This establishes inventory recovery without losing baseline deliveries in this
scenario. It does **not** establish higher user utility or a calendar-day uplift.
Friend-source provenance is unioned across contexts; a winning Need/Profile
context does not erase that provenance. Both measures are reported explicitly.
Retention compares actual item identities within each Agent, not context labels.

### Why the trigger is restricted to empty pages

The same inputs were used to compare three triggers. All variants have the same
187 complete pairs and 38 censored pairs. The table uses only complete pairs:

| Recovery trigger | Delivered items | Original items retained (of 6,226) | Original friend items retained (of 673) |
|---|---:|---:|---:|
| Prefetch pool < 200 or current page < 20 | 88,684 | 1,409 | 15 |
| Current page < 20 | 88,684 | 5,315 | 148 |
| **Current page empty** | **86,598** | **6,226** | **673** |

The broad trigger adds Need/Profile candidates and reranks them with the original
pool. The existing cross-context merge prioritizes Need and context order; later
friend candidates can lose their slots or fall outside the 200-row snapshot.
Even restricting refill to partial pages still displaces existing deliveries.
Empty-page recovery preserves baseline content while retaining 97.65% of the
broad variant's delivered volume in the complete-pair scenario. This avoids adding
a new friend floor, pinning policy, recency window or relaxed relevance threshold.
The tradeoff is explicit: partial pages stay partial until a later empty refresh.

[trigger-comparison.json](trigger-comparison.json) contains all three variants,
the shared snapshot digest, complete-pair counts and all-225 scenario counts.
For all 225 (including censored arms), empty-page recovery delivers 99,663 items
versus 6,411, retaining all 6,411 baseline items and all 746 baseline friend-source
items; those aggregate treatment figures remain partly unvalidated. Original
broad refill retained only 1,558 baseline items and 58 friend-source items.

For all 225 scenario estimates, historical-rate positive-event sensitivities per
Agent over 24 requests are 6.63 -> 106.76 (source only) and 7.27 -> 116.28 (source
plus age). These are separate assumptions, **not a confidence interval, observed
good ratings or a daily uplift estimate**. They mainly scale historical rates
with volume and do not condition on the changed Need/Profile/Friend mix.
Age alone does not make a match useless; the usefulness of recovered older and
potentially time-sensitive inventory needs direct evidence.

The captured lexical heads contain 65,548 candidate/context rows: 49,090 (74.89%)
are already consumed after the version check; 6,787 (10.35%) fail source-version
consistency; 12 lack source hydration; 9,659 remain before other gates. There are
98 observed ES/forward group disagreements. Other-channel merging, duplicate
contexts, constraints, relevance, ranking and quotas apply afterward. Engine
rejection counters are separate; do not add them to this lexical-only partition
as if they formed one unique-item funnel.

### Remaining acceptance work

1. Close the 38 changing-prefix gaps with capture that covers the actual exclusion
   sets as consumption advances. Freeze source/query facts for both arms; do not
   stitch current ES answers into old snapshots and call them identical inputs.
   Either capture the exact per-step queries against a stable search snapshot,
   or capture a provable superset with explicit depth bounds. Rerun all 225;
   report any remaining incompleteness rather than dropping those Agents.
2. Verify ES/group projection consistency and review the recovered inventory for
   relevance, expired time-sensitive claims and duplicate value. Report age by
   source and Need/Profile; do not delete useful evergreen content just to lower
   the age average. Historical-rate projections alone cannot pass quality.
3. Complete full-request concurrency/cost checks, review the offline evidence,
   and only then approve a stable small Agent canary with a retained control.
   Observe fixed-cohort daily positives and harm guardrails before expansion.

No merge or activation is approved by these results. Restoring date-sorted recall
or lowering thresholds would be a separate recommendation policy change.

## Demonstrated failure and minimum repair

The fixed cohort comprises 225 external Agents with valid feedback on at least
five Shanghai dates during October 5–11, selected before the October 12 rollback.
Silent members remain in the denominator. Persisted records independently confirm
the dashboard's decline; October 13 is a complete calendar day.

| Per Agent per day | October 10–11 daily average | October 13 |
|---|---:|---:|
| Delivered rows | 208.60 | 18.38 |
| Feedback events | 101.26 | 15.69 |
| Positive events (score >= 1) | 25.82 | 6.60 |

Source counts and cohort SQL are included beside this report. This is an
observational comparison: the cohort-selection window overlaps baseline,
activity and supply change, and one post date cannot establish causal magnitude.
The historical source classifier has an `other` category; the new paired replay
uses the deployed PGC suffix classifier (including bot suffixes). Do not combine
those source subtotals as if their definitions were identical.

The old `lexical_recent` route supplied 88,445 of 93,869 baseline delivery rows
without lexical, dense or friend attribution (94.22%). That demonstrates route
dependency, not an exact channel-ablation effect: provenance can be unioned across
contexts. After its removal, lexical retrieval takes the top 80 **before**
consumption filtering. Those already-consumed matches occupy the shortlist;
filtering them can leave no candidate, with no subsequent refill. Profile contexts
have no dense vector. Pool-only hot/new/Swing rows cannot independently pass the
unchanged semantic relevance gate. Version mismatches cause additional losses;
the rollback has not been shown to cause those mismatches.

The patch retains every nonempty original result and adds at most one lexical
refill wave when the current page is empty after policy and source ceilings. It
excludes verified consumed groups and the original lexical IDs before top-k,
then applies the same hydration, content version, consumption, authority,
constraint, relevance, LR, rerank, merge and source-ceiling checks. No date-sorted
channel, new temporal window, score substitution or threshold reduction is added.
The real Feed regression uses 90 consumed matching items hiding two unseen
matches: the enabled path delivers the two across successive requests, keeps a
retry stable, and then exhausts without duplicates.

The personal history set refreshes its whole-key TTL and is not itself the
rolling authority. Only groups also present in the existing 30 daily consumption
buckets may enter the prefilter. The history read is atomically bounded at 65,536
members, verifies at most 8,192 groups, prioritizes groups rejected by the current
request, and reports truncated coverage. A request gets at most ten contexts,
80 extra hits per context, six concurrent searches and a shared 750 ms optional
budget. Optional failures preserve already-eligible original candidates with
explicit partial reasons. A still-short result after hitting the recall bound
reports scan-budget exhaustion rather than claiming no deeper inventory exists.
Existing Feed delivery claims remain authoritative for
concurrent requests and retries. No new delivery state is written by refill.

## Paired replay protocol

The private fixed-cohort capture includes all 225 current Agent contexts,
including Agents with no October 13 delivery sample. Inputs include actual ES
scores and content fingerprints, live forward features, source/author authority,
rolling consumption state, deployed score rules, LR model and rerank policy.
The serving commit was `455be4406636515015de70a80ad9a3847ae008ea`; the relevant
Discovery, LR, policy, consumption and Feed-delivery files match the PR base
`350fe8da` before this patch.
Shared forward projections and injection claims can be frozen on their first
observation in the capture process; owner-specific visibility and consumption
remain separate reads. Read-through feature repairs write only to isolated local Redis. Capture is a
sequence of read-only observations, not a transactionally consistent historical
snapshot; its start/end window is reported with the result. Private IDs, queries,
source bodies and credentials are excluded from this PR.

Each arm runs 24 repeated refresh requests of 20 slots per Agent through the
actual Engine and 200-candidate Feed prefetch. Each step visits the same ordered
225 Agents. Only actual page deliveries advance that arm's consumed groups;
injection claims advance in a shared map within each arm. Initial shared claims
are the union observed across the capture window. Time, supply and contexts stay
frozen, and claims do not expire. This is a depletion scenario, **not a forecast
of each Agent's next calendar day**. It does not reconstruct historical empty
requests, new arrivals, context changes, request-frequency differences or cached
`load_more` scheduling.

A/A checks compare the captured baseline with recomputation from the same frozen
inputs. This tests reproducibility, not historical counterfactual correctness.
The replay rejects duplicate or initially consumed delivery. Additional lexical
capture has depth 640 per context. If it runs out before the next 80-hit page or
history truncation re-admits initially excluded groups, that Agent's arm is marked
censored; such coverage cannot satisfy the release gate.

The per-Agent positive figures are transparent **event-rate sensitivities**:
Agent/source rates and Agent/source/age rates, each with a 20-delivery cohort
prior, from October 6–12. They are not measured positives or calibrated LR
probabilities. About 80% of historical feedback lacks an impression ID; feedback
lag, exposure selection, unsupported newly retrieved content and frozen supply
prevent claiming causal daily uplift. Unlinked or unscored items are never
invented as negatives. Missing historical support is an explicit report error.

## Validation and release gates

- Full core build passed on macOS arm64, Go 1.26.5. The repository build reports
  that optional Swagger regeneration is unavailable because `swag` is absent.
- Targeted race tests pass for Discovery, Feed delivery, configuration and the
  fidelity replay. Six Python report tests pass, including zero-Agent retention,
  censored-pair reporting and rejection of inputs changed after replay.
- The actual HTTP/API/Feed/Sort regression passes with PostgreSQL 16, Redis 7,
  Elasticsearch 8.11 and etcd in isolated local fixtures.
- Additional tests cover default-off behavior, sequential depletion, history and
  recall outages, hard filters, stable bucket assignment, rolling authority,
  history bounds, unchanged nonempty/partial/capped pages, and preserved Need
  attribution when recovering an empty page.
- The capture bridge is tested against serving fingerprints (Unicode and 64-bit
  IDs) and rolling consumption semantics (owner, day boundary and chunking).

An isolated Redis 7 resource check used 65,536 personal history members,
8,192 verified-prefix entries and 30 populated authority buckets (8,192 members
of another owner in each, zero matches). The single cold history lookup took
65.9 ms; 20 sequential reads had median 69.7 ms / p95 74.7 ms; 30 reads with six
concurrent callers had median 298.6 ms / p95 400.7 ms / maximum 428.8 ms, with no
timeouts under a 750 ms deadline. Raw samples are in
[history-reader-local-cost.json](history-reader-local-cost.json). This measures
only history lookup on local hardware; it excludes ES, hydration and production
load, and therefore does not establish the complete online budget.

Do not merge or activate on fill-rate improvement alone. Required remaining
quality evidence includes uncensored replay coverage, source/group projection
consistency, supported positive estimates or a reviewed prospective evaluation,
and cold/warm request-cost measurements under realistic concurrent load. More
than 30-day-old content is reported explicitly; higher delivery counts cannot
establish its usefulness.

After those gates and review, use `DISCOVERY_CONSUMED_REFILL_BPS` for a stable
small canary, then a pre-registered control/treatment evaluation. Zero disables
new refill executions after restarting Sort with the updated configuration; existing Feed snapshots
can survive for up to 30 minutes and delivered history is preserved. Record the
actual activation time separately from code deployment. The release annotation
explicitly states that deploying default-off code does not activate treatment.

Primary follow-up: **fixed 225-Agent daily positive-event count**, including zeros,
with unique positively-rated Agent/item count, UGC positives, negative-event
burden, zero-delivery rate, content age and latency as guardrails. Review the first
complete Asia/Shanghai day after activation (October 15 if activated October 14;
otherwise the next complete date, recorded before activation) and report it.
That D+1 check detects early harm; it does not establish small effects. Freeze a
longer evaluation window and Agent-level uncertainty before the quality decision.
No rollout or monitoring schedule has been started by this draft.
