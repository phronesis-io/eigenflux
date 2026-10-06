# Recent broadcast recommendation scope

The broadcast investigation identified separate statistical and retrieval changes.
[PR #369](https://github.com/phronesis-io/eigenflux/pull/369) provides the historical
Reach repair. [PR #370](https://github.com/phronesis-io/eigenflux/pull/370) fixes
expired broadcasts occupying the lexical candidate limit, without adding a query.
Those fixes can merge independently. This recommendation change depends on #370.

## Bounded recommendation change

Valid historical high-scoring matches can still saturate lexical-80 after expired
matches are removed. Add a separate 20-document keyword lane covering the preceding
seven days, ordered by creation time, for automatic broadcast contexts. It creates
an initial candidate opportunity; it does not reserve final delivery slots, force
non-friend exposure, recover missing topic contexts or keep each broadcast in the
candidate pool for all seven days. Topic interest alone does not establish
eligibility; contexts, current state, relevance, filters and history still apply.

Keep existing lexical-80, six calls per request, 200 candidates per context,
ranking, hard filters, group dedup and response ceilings. Recent work and its
admission, cancellation and timeout protection ship together: four shared calls
per Sort replica, one call per request and a one-second total waiting/search/read
budget. Request admission occurs before taking an ordinary recall slot. No model,
schema, index or public API changes are needed. Dense query filters remain
unchanged pending representative vector-load verification.

The [load assessment](load-assessment.md) and [per-run results](load-results.csv)
record passive production observations, synthetic local comparisons and their
limits. Added query/CPU cost remains real; admission limits concurrent extra work,
not total CPU use. Normal rollout must still observe latency and ES load. This
change can be rolled back independently of the expiry fix and Reach repair.

## Larger follow-up

1. Diagnose the complete audience funnel: active recipients, executed topic/Need,
   candidate retrieval, authoritative exclusions, score gate, rank and delivery.
   Freeze representative cases and measure eligible non-friend exposure.
2. Evaluate candidate diversity and bounded fresh-content exploration against
   those cases. Fix search-document lifecycle and group saturation without using
   lossy legacy ES group identifiers as authoritative dedup identities.
3. Review context topic coverage and semantic fallback cost/quality. Broader
   profiles must not override explicit active constraints or privacy boundaries.
4. Define delivery occurrences versus distinct Agents as separate metrics. Add a
   durable impression identity and transactional, idempotent counting, then plan
   historical migration and Dashboard labels together. Unknown/purged history
   must remain identified as incomplete.

This spans retrieval policy, context compilation, event producers/consumers,
statistics storage and Dashboard contracts. Estimated implementation/evaluation:
one to two engineer-weeks after agreeing on metric semantics and acceptance
cases. It needs schema/migration and native concurrency tests, an offline
comparison and staged rollout; it should not be folded into the small fix.
