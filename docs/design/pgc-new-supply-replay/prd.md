# PGC new-supply delivery replay

## Problem and scope

A bounded independent review can rescue a previously discarded PGC article.
The existing candidate-union replay cannot admit that article and therefore
cannot measure which delivered PGC or UGC it displaces. The owner approved
extending offline tooling before enabling the PGC repair.

The replay must freeze current production contexts, serving switches, rules,
models, retrieval pools, authority, consumption, semantic evidence and source
classes. Production currently enables profile semantic recall, rating LR,
consumed refill and a partial Need-budget rollout. Omitting these branches is
not an acceptable baseline. This change modifies offline tooling only.

## Behavior and invariants

The baseline contains no rescued items. The treatment adds only real candidates
that pass the repaired PGC processor and current server extraction, summary,
quality, embedding and grouping rules. Candidate IDs are private replay IDs;
publication time is an explicit counterfactual assumption, never a fabricated
production receipt. New items compete inside the ordinary recall limits,
ranking, Need precedence, group/consumption filters and page source ceilings.
No forced delivery or assumed quality uplift is permitted.

Freeze a recent cohort before inspecting results. Require exact unchanged A/A
reproduction, including active experimental branches. Missing authority,
semantic, grouping or consumption evidence invalidates the run. Model failures
remain visible; they do not become successful unchanged samples. No writes to
production stores, impressions, feedback, queues or delivery APIs are allowed.
Local Elasticsearch writes are restricted to the existing isolated loopback.

## Output and acceptance

For every Agent report before/after delivery counts, PGC/UGC and Need/Profile
mix (friend/baseline remain separate), retained/displaced/new items, and expected
positive events from a fixed recent feedback window. State workload and feedback
lag. Estimated lexical scores and finite corpus limitations must accompany the
report; A/A alone does not prove counterfactual accuracy. Also report extraction
failures and filtered rescues, not just winners.

Tests must establish off-arm isolation, unknown-evidence rejection, recall
cutoffs, consumed-group preservation and unchanged A/A. Integration acceptance
uses actual local search and the production engine. Before release, bind evidence
to deployed code/config/model hashes and the PGC change SHA.

## Rollout and rollback

Keep PGC PR #474 draft and both feature switches off until the delivery gate
passes. No server deployment is needed for this offline tool. The PGC review
has a durable once-per-item limit, a daily cap and the existing total model
budget. Disabling PGC_DISCARD_RECHECK rolls back future review without code or
historical requeue. Querit stays disabled. Check the same cohort's daily positive
ratings the day after rollout; do not claim next-day validation in advance.

Validation evidence and limitations: [validation.md](validation.md).
