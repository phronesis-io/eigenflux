# PRD: Recommendation should not select only 5 Needs

Status: direction confirmed by Pascal on 2026-11-27 (see Section 6a). Implementation stays draft until the offline replay evidence exists; no merge or release without Pascal's further confirmation.
Baseline: `origin/main` fc898a99 (2026-11-27). The version actually deployed on aliap has not been checked and must be confirmed before release.
Impact queries: `impact-queries.sql` in this directory (Grafana read-only datasource `grafana_ro_v2`, 2026-11-27).

## 1. User problem

An Agent can hold many Needs, but each feed pull uses only 5 of them. The rest are never recalled.

Example: an Agent with 43 active Needs across 10 Intents never received major news that was already indexed and ranked high in search. Across 8 locally saved Feed pulls, the same 5 Needs appeared every time: the 5 most recently created.

## 2. Current behavior (verified on origin/main)

- Selection: `Store.Active` in `pkg/need/reader.go` still ends with `LIMIT 5`. It first gives each requested kind one slot, then orders by `priority` desc, `created_at` desc, `need_input_id` asc. Feed requests only `broadcast`, so the result is the top 5 by priority, then newest.
- Call chain: Feed pull -> `ServePage` (`rpc/feed/delivery/page.go`) -> executor runs `legacy_prefetch` on Sort -> `service.go` maps it to Recommendation + Prefetch -> `contexts()` in `engine.go` -> `CachedNeeds.Active` (30 s cache) -> `Store.Active`.
- Explicit `need_ids` requests are also capped at 5 (`compiler.go`), so the limit exists in more than one place.
- `ratingMaxNeeds = 16` (`rating.go`) is checked after selection has already cut to 5, so it is not the bottleneck today. Raising the selection cap above 16 would make ranking fail with `need_budget`.
- `need_mix` and relative attribution (`DISCOVERY_NEED_RELATIVE_BPS`, default 0) only share page slots among the 5 selected Needs. They do not help unselected Needs.
- A second, separate cap: page slots are allocated by priority tier. `Merge` in `rpc/sort/discovery/score.go` sorts captured-Need candidates by `Context.Priority` descending before comparing any score, and the `need_mix` rotation (`mixNeeds`) only runs inside one equal-priority tier. When selected Needs have different priorities, the highest-priority Need fills the page first, and lower tiers get slots only if it runs out of eligible candidates. Observation (one sample, reported by the sibling session on 2026-11-27): after an Agent's 5 Needs were given priorities 1.0/0.9/0.8/0.7/0.6, all 20 items of one pull came from the priority 1.0 Need; before priorities were set, the split across the 5 Needs was 9/7/2/1/1. Production count (2026-11-27): 59 Agents have at least one Need with a priority; 54 have mixed priority levels, of which 21 have more than 5 Needs and 33 have 5 or fewer. For these 54 Agents the highest-priority Need can take every page slot, so rotating the 5-Need window alone does not fix them, and Agents with 5 or fewer Needs are affected too. The code default of `DISCOVERY_NEED_RELATIVE_BPS` is 0 (`pkg/config/config.go`, `.env.example`); the production value has not been verified. Even when it is enabled, `mixNeeds` only rotates inside one priority tier. This needs its own decision: for example, cap the share of the page any single Need can take, or treat priority as an ordering hint rather than an exclusive tier. It is listed as an open question below.

## 3. Impact (current production data)

- 1,136 Agents hold 4,236 broadcast Needs. 236 Agents (20.8%) have more than 5; 587 Needs (13.9%) are cut.
- Distribution: median 3, p90 7, p99 10, max 34. Of the 236, 235 have 6-10 Needs and one has 34.
- 564 Intents (13.4% of all) have no selected Need at all under their Agent.
- `priority` is almost always empty (4,123 of 4,237). For 215 of the 236 Agents all Needs share one priority, so selection is simply the 5 newest.
- Last 7 days (11-20 to 11-26) of recommendation deliveries: 93 active Agents have more than 5 Needs, 698 Needs in total, of which 233 (33%) cannot be recalled now. Only 438 of the 698 were delivered at least once.
- Positive-rating baseline (same window, Need-sourced deliveries): Agents with 6+ Needs get 25.4 positive ratings per Agent per day (59.0% of rated); Agents with 1-5 Needs get 26.6 (60.0%).
- Limits: the cut count uses the current Need set, not a snapshot from 7 days ago. Unrated deliveries are excluded from the rate.

## 4. Scope

In scope: automatic Need selection on the Feed recommendation path (`legacy_prefetch`), only for Agents with more than 5 Needs.
Out of scope: recall channels, ranking models, `need_mix` parameters, search, and Agents with 5 or fewer Needs (their behavior must stay byte-identical).
Pending decision (question 5 in Section 10): the priority-tier problem from Section 2 also affects 33 Agents with 5 or fewer Needs. If it is brought into this PRD, the byte-identical guarantee for 5 or fewer Needs must be narrowed to Agents whose Needs share one priority level, and the scope and replay cohort must be extended accordingly.

## 5. Options

| Option | Approach | Pros | Risks / cost |
| --- | --- | --- | --- |
| A. Raise the cap | 5 to 8, 10 or 16 | Smallest change | Each extra Need adds one context compile and two dense recalls (80 + 40 candidates), so cost and latency grow; cap 10 covers p99 but not the largest Agent; above 16 triggers `need_budget`; each Need gets fewer page slots |
| B. Rotate Needs | Still 5 per request, but the window slides across requests so every Need gets a turn | Per-request cost unchanged; covers all Needs over time | Each Need appears in only some requests; needs a stable rotation key that works with the 30 s cache and frozen pages |
| C. Allocate by Intent | Give each Intent one slot first, then fill by priority and recency | Matches the Agent's real intent structure | Still static: with more than 5 Intents, some never get a slot |

## 6. Recommendation

B with C's Intent grouping deciding the rotation order:

1. Needs with priority > 0 are always selected, up to 3 slots.
2. Remaining slots are filled by taking Intents in turn; within an Intent, newest first.
3. The rotation offset is `hash(agent_id, time_slice)`, default slice 1 hour: stable inside a slice, sliding across slices.

Reason: per-request cost and latency stay as they are today (A cannot do this), and every Need gets covered (C alone cannot). The 235 Agents with 6-10 Needs are covered in 2 slices; the 34-Need Agent in 7.

Switch: `DISCOVERY_NEED_ROTATION_BPS`, default 0, range 0-10000, stable per-Agent bucketing, restart Sort to apply. Agents with 5 or fewer Needs never enter the new path. Rollback: set 0 and restart.

## 6a. Confirmed direction and final design (supersedes Section 6 where they differ)

Pascal confirmed on 2026-11-27: lift the 5-Need cap and fix the highest-priority tier taking the whole page. Everything below sits behind one switch, `DISCOVERY_NEED_ROTATION_BPS` (default 0, range 0-10000, stable per-Agent bucket, restart Sort to apply). Rollback is setting it to 0.

1. Selection moves after the cache. `CachedNeeds` caches the full list of eligible broadcast Needs (new `need.Store.Eligible`, same eligibility as `Active`, no LIMIT). The 30 s cache therefore cannot pin a rotation window, and control Agents keep calling the unchanged `Store.Active`.
2. Window size is 8 Needs per request, raised from 5 and within `ratingMaxNeeds = 16`. Needs with priority > 0 are pinned, at most 4. The remaining slots rotate across Intents, newest first inside an Intent, with offset `hash(agent_id, hour_slice)`. An Agent with 8 or fewer Needs gets all of them.
3. For treatment Agents, priority is no longer an exclusive page tier. Selected Needs compete in one tier, `need_mix` and relative attribution apply to that bucket, and priority only decides pinning. Control keeps today's ordering.
4. Cost: each extra Need adds one context compile and two dense recalls (up to 120 candidates); the 240-candidate context cap stays. Replay must report latency for the 34-Need Agent.
5. Reference point from the inbox session: one Agent with 35 active Needs, 5 of them tied at priority 1.0, whose pull split 7/6/4/2/1 across those Needs. Replay must include this Agent.

The byte-identical guarantee now reads: Agents with 5 or fewer Needs and a single priority level stay identical; Agents with 6+ Needs or mixed priorities (54 Agents) change in treatment only.

Replay evidence required before the PR leaves draft: per-Agent before/after delivery mix (PGC/UGC, Need/Profile), expected deliveries and positive ratings, using `scripts/discovery_fidelity_replay` on the cohort of Agents with 6+ Needs plus the 54 mixed-priority Agents.

## 7. Metrics

- Primary: daily positive ratings of a fixed cohort (the 93 active Agents with more than 5 Needs), against the baseline of 25.4 per Agent per day.
- Coverage: share of the cohort's active Needs delivered at least once per day; baseline 63% (438/698).
- Guardrails: positive rate not below 57% (baseline 59% minus 2 points); Sort p95 latency not higher; delivery mix for Agents with 5 or fewer Needs unchanged; PGC/UGC and Need/Profile mix within the replay forecast.
- When: starting the day after each rollout step, for 3 consecutive days, on the same cohort.

## 8. Acceptance

1. Unit tests: 5 or fewer Needs is byte-identical to today; window is stable within a slice and slides across slices; priority Needs are pinned; Intent grouping and order are deterministic.
2. Offline replay with `scripts/discovery_fidelity_replay` on the cohort's recent contexts, reporting before/after delivery mix (PGC/UGC, Need/Profile) and expected deliveries and positive ratings per Agent. If this cannot be produced, the PR stays draft.
3. In the replay, Agents with 5 or fewer Needs match the baseline byte for byte.
4. Logs show the selected Need set changing across time slices.

## 9. Release and rollback

Roll out 0 -> 10% (stable buckets) for 3 days, then widen step by step; each step states the metric and the day it is checked. Rollback is the switch only, no code change; frozen pages keep their content until they expire. The PR states the primary metric, guardrails and check dates.

## 10. Questions for Pascal (2026-11-27: Pascal confirmed lifting the cap and fixing the priority tier; Section 6a records the resulting design. Remaining details below are decided there or by the replay evidence.)

1. Approve B+C, or first raise the cap to 10 (option A)?
2. Should Needs with priority > 0 always be selected? About 114 Needs have a priority today.
3. Is a 1-hour time slice right? Shorter slices cover faster but make recommendations less stable within a day.
4. Is the rollout pace (10% -> 50% -> 100%) and the gate (positive ratings not below 97% of baseline) acceptable?
5. Should this PRD also cover the priority-tier problem from Section 2 (54 Agents with mixed priorities, 33 of them with 5 or fewer Needs, where the highest-priority Need can take all 20 page slots)? Options: (a) leave it out and handle it in a separate PRD, because it changes page allocation rather than Need selection; (b) cap the share of a page any single Need can take, for example 50%; (c) treat priority as an ordering hint inside the rotation instead of an exclusive tier. Before deciding, the production value of `DISCOVERY_NEED_RELATIVE_BPS` must be checked on aliap, and the offline replay must include these 54 Agents.
