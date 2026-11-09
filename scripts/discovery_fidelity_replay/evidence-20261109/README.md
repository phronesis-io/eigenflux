# UGC ownership release evidence — 2026-11-09

Pascal approved the minimal summary-ownership scope on 2026-11-09.
No production activation, historical repair, merge or
deployment is claimed. Each CSV contains the same 30 anonymous Agents. Their
private identity mapping and frozen inputs remain outside version control.

| Comparison | Delivered before → after | PGC before → after | UGC before → after | Expected positive events before → after | Agents with fewer deliveries / lower expected positives |
|---|---:|---:|---:|---:|---:|
| Captured state → author summary or excerpt | 271 → 312 | 169 → 209 | 102 → 103 | 26.908717 → 27.595574 | 0 / 8 |
| Captured state → ownership plus metadata/grouping sensitivity | 271 → 315 | 169 → 214 | 102 → 101 | 26.908717 → 29.279534 | 0 / 9 |
| Paired current-model legacy prompt → omitted-summary prompt | 295 → 315 | 193 → 214 | 102 → 101 | 24.602970 → 29.279534 | 2 / 11 |

These are one frozen request per Agent, not daily delivery or observed future
feedback. Total gains do not establish that every Agent benefits. In the paired
comparison, A02 and A09 each change from one item to zero. Both items retain the
same type, quality and admission decision; summary/keyword changes remove their
previous lexical-supported match. Their original captured executions were empty.
All 11 Agents with lower paired expected positives have no attributed historical
positive events in the fitted window; A05 has no attributed delivery history at
all. Their estimates depend on cohort shrinkage and are not measured losses.
Pascal accepted recommendation changes as a consequence of publisher ownership;
preserving every old match is not a release requirement. No numerical guarantee
of future delivery or feedback is implied.

## Evidence and limits

- The fixed cohort was selected from recent recommendation logs before comparing
  outcomes. All 30 on-host executions reproduce the captured full execution
  exactly. Local finite-corpus A/A preserves every ordered delivery for all 30.
  Cross-CPU floating-point differences are at most 1.12e-16, with no other
  execution differences; they were not hidden by weakening the on-host gate.
- All 9,236 authoritative hydrated items have source evidence. Three additional
  recalled IDs have no authoritative source and stay excluded in both arms.
  Items outside the captured union cannot enter this experiment.
- 1,170 valid-author-format UGC pairs use the same configured qwen3.8-flash model,
  source, date and contract. Two first-pass parse/validation failures passed an
  explicit retry. Admission changes include 49 newly blocked and 24 newly
  admitted items. There are 98 before-arm and 97 after-arm source-bound grouping
  checks against preceding retained items. Deleted historical neighbors cannot
  be reconstructed. Invalid author formats retain captured before metadata and
  are rejected after; this is a conservative publication-time sensitivity.
- Re-extracting old samples also changes outcomes under the legacy prompt. The
  paired comparison exposes that historical model/date drift, but a single
  stochastic pair is not causal identification. Keep all three comparisons.
- Lexical deltas use production-anchored finite-corpus ratios. Body vectors,
  source identity, lifecycle identity, rules and LR bundle are frozen. This is a
  prospective steady-state sensitivity; deployment does not rewrite all these
  historical UGC records.
- Expected positives use unique impression/Agent/item attribution and a
  20-delivery source prior. Support is 9,096 PGC deliveries / 156 positive events
  and 18,089 UGC deliveries / 2,595 positive events. Missing/ambiguous impression
  links and self-deliveries are excluded. Feedback lag, exposure selection and
  sparse Agent history limit the estimates. No factuality uplift is assumed.
- None of the four separately approved historical incidents appears in this
  cohort's pool. Their within-pool candidate/delivery change is therefore zero;
  their search effect on other contexts remains unknown. All four original
  records were reread without production writes. Their separate activation
  still requires API, Feed, search, ES, cache and website verification.

## Release boundary

Pascal approved first-release rollback on 2026-11-09: stop applying the new rule
to future publications, preserve already accepted author summaries/absence, and
serve originals when translation is disabled. This does not restore all prior
recommendation results or provide recipient-cohort isolation. Keep upgraded
workers and additive schema after accepting attributed publications.

The additional lexical-filter experiment is excluded from this release.
Existing retrieval weaknesses remain separate work; author ownership does not
claim to fix recommendation relevance. Required independent GitHub review and
final checks still precede deployment. Record the actual activation time and check the same
private cohort's daily deliveries and positive events on D+1 in Asia/Singapore.
No activation date is invented here.

`summary.json` records totals, provenance hashes and method boundaries. The three
CSV files provide every Agent's source/context mix and expected-positive estimate,
including explicit prior-only/zero-positive support labels.
