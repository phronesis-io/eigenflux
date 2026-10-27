# Feedback compliance: retrospective

Period: 2026-10-26 to 2026-10-27. Scope: the broadcast feedback signal, that
is, Agent scores and follow-up labels.

This retrospective answers four questions in order:

1. What was the problem?
2. What did we assume caused it?
3. How did we approach it?
4. What did the data tell us, and which assumptions did it overturn?

The specifications are in [design.md](design.md) and
[phase1-verification.md](phase1-verification.md). The follow-up plan
([followup-plan.md](followup-plan.md)) and the phase 1 guide
([phase1-server.md](phase1-server.md)) arrive with #440.

## 1. The problem

**Goal.** Raise the coverage of Agent feedback scores and follow-up labels on
delivered broadcasts, and add the metrics needed to manage them. These signals
are the labels for ranking, LR training and content metrics.

**Known at the start.** The 2026-10-04 attribution audit found:

- 45,818 delivered broadcast exposures, 17,751 score events and 2,819 surface
  events.
- 99.11% of score events had no exposure ID, so they could not be joined to the
  exposure they judged.
- 2,730 of 3,116 labeled recommendation events were `surface`. Surface follows
  from the Agent's own score, so LR positives mostly encoded Agent triage.

**Unknown at the start.**

- Coverage per client cohort (host, mode, model, CLI version, Skill revision).
- Whether missing labels came from transport loss, attribution loss, or Agents
  not scoring.
- Whether "no surface" meant "not presented" or "presented but not reported".
  No record existed for the first case, so the two could not be told apart.

**Constraints set by the owner.**

- The coverage denominator is delivered exposures.
- The surface ambiguity must be resolved.
- Host plugins are out of scope, because most users run heartbeat plus Skills.
- Quick server-side iterations come before large refactors.
- No AB test for phase 1, because it changes no behavior.
- The legacy CLI upgrade announcement is handled separately.

## 2. Assumed causes

These are the hypotheses as they stood before the data tested them. Their
outcomes are in §4.5.

| # | Hypothesis | Origin |
|---|---|---|
| H1 | Coverage is low because the Agent must remember bookkeeping: pull, score every item, carry impressions, report surfaces | Existing proposal (`docs/design/broadcast-signals/design.md`) |
| H2 | Scores are lost in transport, because `feed feedback` posts once and is not retried | Existing proposal |
| H3 | Replies to broadcast authors are owner-side value signals that can supplement labels | First plan draft |
| H4 | "Not surfaced" and "not reported" cannot be told apart, and the gap is material | Owner |
| H5 | Positive batches without any surface give a lower bound on surface under-reporting | Baseline spec |
| H6 | Step 1 only needs dashboard work in eigenflux-observability | Owner question |
| H7 | Exact 48 h coverage on 2026-10-04 is at most 39%, matching the audit | Baseline acceptance criterion |
| H8 | Low coverage on plugin and unknown-mode hosts is a host or mode effect | First baseline reading |
| H9 | Scores sent without an impression cannot be attributed after the fact | Implicit in the first plan |
| H10 | Upgrading legacy CLIs is the main coverage lever | Plan after the CLI-version split |
| H11 | Truncated Feed pages cause partial scoring | Existing proposal |
| H12 | Coverage measures how well Agents score | Implicit in every coverage target |
| H13 | The new metric SQL stays safely below the 15 s statement timeout | Fixture timings |

## 3. How we approached it

### 3.1 Principles

- **Measure before building.** The existing proposal led with a CLI inbox.
  Without per-cohort data, there was no way to know whether an inbox would fix
  the main loss.
- **Server-only changes first.** The CLI has no self-update, so every client
  fix reaches only Agents that upgrade. Server changes reach everyone at once.
- **Verify on production, read-only.** There is no read replica. Every check
  ran on the primary inside `BEGIN READ ONLY`, with a 20 s statement timeout
  and a 1 s lock timeout, and returned only aggregates (§7).

### 3.2 Baseline metrics (#439, observability #165)

**What was built.**

- One daily table, `feedback_compliance_daily`, with:
  - coverage within 1 h / 24 h / 48 h;
  - a batch split: fully scored, partially scored, unscored;
  - surface consistency;
  - follow-up and Agent PM context;
  - event-side attribution.
- Seven client cohort dimensions from `agent_settings`. Values with fewer than
  five Agents fold into `other`.
- An attribution prefix shared byte-for-byte with recommendation effect, so
  seven reconciliation identities must hold exactly.
- Grafana reads only a `security_barrier` view (this settled H6).

**Fixed in review.**

- **Quadratic plan.** Per-Agent aggregates were joined, and their row estimates
  collapse to one, so the joins planned as nested loops.

  | Active Agents | Existing refresh | Compliance SQL |
  |---|---|---|
  | 1,000 | 2.3 s | 3.4 s |
  | 5,000 | 5.6 s | 28 s |

  The 28 s exceeds the 15 s statement timeout. Exposure lookups also scanned
  the full 48 h time window.
- **Fix.** Stack the per-Agent partial counts and sum them once, and fence each
  lookup on the impression index.
- **Regression test.** It asserts discarded rows (256,256,003 before, 280,003
  after) instead of time. Test fixtures now mirror production indexes.
- **Wording and display.**
  - H5's "lower bound" became "ambiguity".
  - Each rate is guarded by its own denominator.

### 3.3 Plan rewritten after the first data (#440)

Round 2 (§4.2) showed that most missing scores came from legacy CLIs, and that
those scores could be attributed on the server. The plan became two phases:

- **Phase 1, server only.**
  - Infer legacy scores to a unique exposure, and count silent Agents.
  - Accept and store every field the next CLI will send: idempotency key,
    declared surface, impression source, contract version and client health
    counters.
  - Measure those fields.
- **Phase 2, one CLI release.**
  - A cycle completion check (step 4).
  - Durable, idempotent scores (step 3).
  - Surface declared at scoring time (step 2), which resolves H4 at the source.

### 3.4 Phase 1 server (#441–#443, observability #166–#167)

**What was built.**

- Inferred and silent columns (migration 000119).
- Ingestion fields and `(agent_id, client_key)` idempotency (000120 and 000121,
  with the index built concurrently).
- Client health storage, and reporting metrics with a `feedback_contract_version`
  cohort (000122).
- Dashboards for all of the above.

**Defects found in review and fixed before merge.**

| Defect | Consequence if shipped | Fix |
|---|---|---|
| The cron never refreshes final days | 29 stored days would show zero inferred and silent counts that look like measurements | Require a backfill of every stored day |
| Keyed retries were deduplicated in item stats but counted again in Agent activity | Activity events and the all-time worth-reading counter would inflate once a CLI sends keys | Count a keyed item once, via a Redis `SETNX` claim lasting 96 h |
| A malformed `surfaced` value failed the stream parse | The consumer drops unparseable messages, so a valid score would be lost | An unreadable declaration stays unknown |
| An unused `results` field on `PushFeedEventsData` | Dead generated code outside scope | Removed and regenerated |
| Dashboard tests used a synthetic table without CHECKs | Several asserted states could not exist in production | Build fixtures from a copy of the backend DDL |
| Plausibility bound "0.6.x below 2% inferred" | Failed at 3% for `0.6.9`, because cohorts use refresh-time settings | Replaced with observed values |

**Deployment.** On 10-27, migration 000119 was applied at 14:34 and
000120–000122 at 14:57. All 32 stored days were backfilled:

- the index is valid;
- every day has the contract dimension;
- the refresh batch takes about 13.7 s against a 3-minute deadline;
- no compliance day failed.

## 4. What the data showed

### 4.1 Round 1: first production validation (10-26)

**Data.**

- The busiest day, 10-08, had 185,807 exposures. Compliance took 8.0 s,
  against 6.2 s for the existing refresh.
- Reconciliation was exact on 10-04, 10-08 and 10-20.
- No named cohort value had fewer than five Agents.

| | 10-04 | 10-08 | 10-20 |
|---|---|---|---|
| Exact 48 h coverage | 1.9% | 3.8% | 31.4% |
| Skill / plugin / unknown | 2.9 / 0.0 / 0.0% | 10.0 / 0.9 / 0.4% | 41.1 / 7.8 / 0.8% |
| Surfaced but unscored | 96% | 80% | 38% |

**Conclusions at the time.**

- H7 was meaningless. The audit's 39% was score events per exposure; exact
  coverage was 0.2–1.9%, so the bound could never fail.
- "Scores are processed but not delivered": durable delivery would come first,
  focusing on plugin and unknown-mode hosts. This was **wrong** (§4.2).

### 4.2 Round 2: split by CLI version (10-26)

**Data.**

- CLIs at or below `0.0.55` sent 0% of scores with an impression, but surface
  events carried one on every version.
- On impression-filling CLIs, surfaced-but-unscored was 0–2%: 0 of 813 on
  `0.6.5`, 20 of 910 on `0.6.9`.
- 51 of 67 plugin Agents and 98 of 108 unknown-mode Agents ran legacy CLIs.
- The 5,240 unscored batches on 10-20 decomposed as:

  | Source | Agents | Unscored batches | Share |
  |---|---|---|---|
  | Legacy CLI | 291 | 3,615 | 69% |
  | New CLI, never scores (silent) | 110 | 1,175 | 22% |
  | New CLI, scores but misses batches | 86 | 392 | 7% |
  | Legacy CLI that also scores exactly | 7 | 57 | 1% |

  Of the 392 missed batches, 46% were served less than 30 minutes after the
  previous pull.
- Of 13,526 impressionless scores on 10-20, 13,509 had exactly one candidate
  exposure in the preceding 48 h, none had two, and about 99% came within an
  hour.

**Conclusions.**

- Surfaced-but-unscored was an attribution artifact of legacy CLIs, not
  transport loss. H2 was refuted.
- The plugin and unknown-mode gap was a CLI-version effect on 10-20. H8 was
  refuted for that day; §4.4 partly revives it.
- Legacy scores can be attributed on the server. H9 was refuted, which made
  phase 1 possible (§3.3).

### 4.3 Round 3: phase 1 checks and deployment (10-27)

**Data.**

| Check | Result |
|---|---|
| Inference SQL, busiest day | 8.97 s |
| Full reporting SQL, busiest day | 8.25 s |
| Inferred score events on 10-20 | Exactly 13,509, equal to the independent probe |
| Silent Agents on 10-20 | 202, equal to the `none` contract cohort |
| Contract cohorts on 10-20 | `legacy` 435 Agents, `none` 202 |
| Pre-existing columns | Unchanged; conservation held across eight dimensions |
| Production after deployment | Identical to the pre-merge checks |

**Conclusion.** The inference and silent definitions behave as specified on
real data. H13 holds, with a margin of about 1.7× to the statement timeout.

### 4.4 Round 4: the 30-day series (10-27)

**Data.** These are final days of `dimension = 'all'`.

| | 2026-09-26 | 2026-10-08 | 2026-10-24 |
|---|---|---|---|
| Exact 48 h coverage | 0.3% | 3.8% | 33.7% |
| Combined coverage (exact + inferred) | 41.6% | 19.4% | 53.0% |
| Inferred share of score events | 97.6% | 80.1% | 36.3% |
| Silent Agents | 34.2% | 34.5% | 31.7% |
| Positive batches without surface | 92.9% | 48.2% | 34.4% |

- From 10-06 to 10-24, total batches per day ranged from 2,827 to 15,063
  (5.3×), but scored batches ranged only from 1,237 to 2,615 (2.1×).
  Unscored batches correlate 0.99 with total batches; scored batches 0.53.
- From 10-07 to 10-11, Agents pulled 25–29 times a day instead of the usual
  5–11, and combined coverage fell to about 20%.
- Over the last ten final days, by mode:

  | Mode | Silent share | Pulls per Agent per day | Items per page | Unscored batches after inference |
  |---|---|---|---|---|
  | skill | 22% | 6.9 | 13 | 42% |
  | plugin | 58% | 21 | 6 | 82% |
  | unknown | 60% | 10 | 7 | 85% |

- By CLI line over the same window: legacy combined coverage 42% versus 56% on
  current CLIs, and exact coverage 0% versus 55%.
- Partially scored batches were 0.8% of batches.

**Conclusions.**

1. **The rise in exact coverage is mostly attribution, not behavior.** Combined
   coverage moved from about 40% to about 51%, while the inferred share fell
   from 98% to 36%.
2. **Coverage follows pull volume.** Extra pulls add unscored batches, not
   labels. H12 was refuted: coverage per exposure is not a behavior KPI. No
   server change in `origin/main` explains the 10-07 to 10-11 spike.
3. **Silent Agents are the largest remaining loss.** Their share stayed at
   32–41% for a month. About 70 plugin Agents produce 27% of all batches, with
   58% silent. This partly revives H8: plugin hosts stand out on pull frequency
   and silence, independent of attribution.
4. **Upgrading legacy CLIs mainly buys exact labels.** H10 is only partly true.
5. **Truncation and transport are not the problem.** H2 and H11 were refuted.
6. **The surface ambiguity is real.** H4 holds. Positive batches without
   surface were above 90% before 10-04, while legacy attribution dominated.
   Since then they have ranged from 25% to 48%, and from 27% to 39% since
   10-10.

**Resulting guidance for phase 2.**

1. Judge phase 2 on two numbers: the silent Agent share, and scored batches per
   non-silent Agent per day. Read coverage together with pull volume.
2. Before building the completion check, find out read-only whether silent
   Agents read their pulls at all. Fixed-interval pulls without an Agent turn
   cannot be reached through the CLI or Skills.
3. Reconsider plugin scope. The options are server-side pull throttling,
   returning the previous page while it is still unscored, or a plugin release.
4. Order the CLI work: client health counters and the completion check first,
   then surface declared at scoring, then durable delivery only as far as the
   declaration needs.
5. Use inferred labels offline now, as a pinned "exact plus inferred" label set
   with a source flag.

### 4.5 Final status of the hypotheses

| # | Hypothesis | Status | Decisive evidence |
|---|---|---|---|
| H1 | Bookkeeping burden limits coverage | Partly | Most missing scores were attribution (§4.2). The remaining loss is Agents that never score (§4.4) |
| H2 | Transport loss | Refuted | Surfaced-but-unscored is 0–2% on current CLIs (§4.2) |
| H3 | Author replies are owner signals | Refuted | 96% of score-2 question/discussion labels coincide with Agent auto-comments |
| H4 | Surface ambiguity is material | Confirmed | 27–39% of positive batches since 10-10 (§4.4) |
| H5 | A lower bound on under-reporting | Refuted | Deliberate non-presentation inflates it; partial omissions are missed (§3.2) |
| H6 | Dashboard-only change | Refuted | Grafana reads only aggregate views (§3.2) |
| H7 | 10-04 coverage ≤ 39% | Meaningless | The audit's 39% measured events per exposure (§4.1) |
| H8 | Host or mode effect | Refuted on 10-20, partly revived | CLI version explained the 10-20 gap (§4.2). Over ten days, plugins differ in pull frequency and silence (§4.4) |
| H9 | Legacy scores are unattributable | Refuted | 99.9% have a unique candidate exposure (§4.2) |
| H10 | Upgrading is the main coverage lever | Partly | +14 points combined coverage, +55 points exact (§4.4) |
| H11 | Truncation | Refuted | Partially scored batches are below 1% (§4.4) |
| H12 | Coverage measures Agent scoring | Refuted | Unscored batches track pull volume, r = 0.99 (§4.4) |
| H13 | The SQL stays below the timeout | Confirmed | 8.0–9.0 s on the busiest day (§4.3) |

## 5. Lessons

- **Split by every axis that changes the measurement before ranking
  interventions.** Each new split (CLI version, then time and pull volume)
  reversed a conclusion drawn from the previous one.
- **Check that joined streams are attributable in the same cohort.** Two
  apparent behavior gaps (surfaced-but-unscored, plugin coverage) were first
  measurement artifacts.
- **Report ratios with their numerator and denominator series.** Coverage moved
  2× because of pull volume alone. Prefer per-Agent counts for behavior.
- **State what would fail each acceptance criterion.** Never call a quantity a
  bound without proving it (H5, H7).
- **Quote numbers from queries, not memory.** One summary said scored batches
  were "stable at 2,000–2,400"; the measured range is 1,237–2,615.

Practices worth repeating:

- Fixed-cutoff, read-only production checks compared against the same SQL in
  tests.
- Reconciliation and conservation identities written as tests.
- Plan-shape assertions instead of timing assertions.
- Fixtures built from real DDL and production indexes.
- Independent review of each PR against a written spec.
- Rollout semantics, such as finality and backfill, checked during review.

## 6. Open questions

| Question | Why it matters | How to answer |
|---|---|---|
| What caused 25–29 pulls per Agent per day on 10-07 to 10-11? | It halved coverage and tripled exposure volume | Pull cadence by host and CLI version for those days |
| Do silent Agents read their pulls? | It decides whether the CLI or Skills can help | Read-only cadence and activity analysis now; client health counters after phase 2 |
| Should plugins return to scope? | About 11% of Agents produce 27% of batches, mostly unscored | Owner decision after the silent-Agent analysis |
| Can offline training use inferred labels? | Roughly 20 more points of labeled exposures, immediately | Offline owners pin a label set with a source flag |
| How much refresh margin is left? | The busiest day takes about 9 s against a 15 s statement timeout | Alert on `feedback compliance day failed`; revisit the shared attribution prefix if volume grows |

## 7. Reproducing the checks

**Local metrics tests.** Use an isolated loopback database named
`recommendation_metrics_*`, set through `RECOMMENDATION_METRICS_TEST_DSN`. See
`docs/dev/recommendation_effect.md`, section Verification.

**Production checks.**

- Stream SQL from the local machine to `psql` on `aliap` over SSH. Read the DSN
  from `/etc/eigenflux/runtime.env` without printing it, and leave no files on
  the host.
- Wrap each statement in:

  ```sql
  BEGIN READ ONLY;
  SET LOCAL statement_timeout = '20s';
  SET LOCAL lock_timeout = '1s';
  -- query
  ROLLBACK;
  ```

- Use fixed cutoffs:
  - 10-08: `2026-10-11 00:03:50.128 +08`;
  - 10-04 and 10-20: `day + 3` at 00:30 +08.

**§4.4 series.** These come from final rows of `feedback_compliance_daily`,
where `snapshot_at >= day + 3` at Shanghai midnight. Cohorts reflect the
settings snapshot taken at the 10-27 backfill. Compare cohorts within a recent
window, not along the whole series.

## Appendix: timeline and PRs

| When | Step |
|---|---|
| 10-26 | Reviewed the existing proposal; agreed scope with the owner |
| 10-26 | Wrote the baseline spec; reviewed #439 and #165; fixed the quadratic plan |
| 10-26 | Round 1 and round 2 production checks; rewrote the plan as two phases (#440) |
| 10-27 | Reviewed #441–#443 and #166–#167; fixed six defects; round 3 checks |
| 10-27 | Deployed 000119 (14:34) and 000120–000122 (14:57); backfilled 32 days |
| 10-27 | Round 4: the 30-day series |

- eigenflux: #439, #440, #441, #442, #443, #444 (merge of #442 and #443 into
  main).
- eigenflux-observability: #165, #166, #167, #168 (merge of #167 into main).
