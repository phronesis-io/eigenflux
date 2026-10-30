# UGC Slop gate sensitivity — 2026-10-30

**Not approval for production activation or global model replacement.**

Ten fixed recent Agent contexts were captured with complete recall pools, policies, seen state and source metadata. Exact A/A replay reproduced every baseline execution. Five requests had zero eligible deliveries; they remain in the cohort. The union contained 2,355 source items; all 454 potentially eligible UGC inputs received a valid result under `qwen3.8-flash`, temperature 0 and explicit reasoning none: 398 non-Slop, 54 Slop and 2 ordinary discards. These are model decisions, not human-labeled accuracy.

Capture times ranged from 2026-10-30T11:28:08.250000+00:00 to 2026-10-30T11:48:52.518000+00:00. Recent contexts were combined with live pools at capture time; this is not a reconstruction of the historical serving pools.

| Measure | Before | Gate-only after |
|---|---:|---:|
| Deliveries | 89 | 89 |
| PGC | 43 | 47 |
| UGC | 46 | 42 |
| Need | 20 | 20 |
| Profile | 69 | 69 |
| Estimated positive events | 12.923059 | 12.562875 |

The historical-rate estimate decreases by **2.79%**, with three Agents lower and no Agent losing delivery count. It does not model higher approval probability from removing Slop and is not observed online feedback or a causal forecast. It also does not replay changed summaries, keywords, quality for non-Slop, context generation or a global model migration. This missing scope and the negative sensitivity must be reviewed before activation.

Estimates use one frozen request per Agent, source-stratified historical positive-event/delivery rates and a 20-delivery empirical-Bayes prior. History is 2026-10-22 through 2026-10-29 (exclusive), UTC+8. Feedback includes unmatched impression events and event lag; expected positive events are not unique positively rated items. This is not a daily-volume forecast.

| Agent | Deliveries before→after | PGC before→after | UGC before→after | Need before→after | Profile before→after | Expected positive events before→after |
|---|---:|---:|---:|---:|---:|---:|
| 339338708664713216 | 20 → 20 | 13 → 15 | 7 → 5 | 0 → 0 | 20 → 20 | 2.193733 → 1.982201 |
| 359904276295712768 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0.000000 → 0.000000 |
| 360979555462152192 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0.000000 → 0.000000 |
| 361428400298000384 | 9 → 9 | 0 → 0 | 9 → 9 | 0 → 0 | 9 → 9 | 0.084289 → 0.084289 |
| 368600644946231296 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0.000000 → 0.000000 |
| 371130135526506496 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0.000000 → 0.000000 |
| 371512333609992192 | 20 → 20 | 3 → 4 | 17 → 16 | 0 → 0 | 20 → 20 | 3.925215 → 3.779812 |
| 372327066533625856 | 20 → 20 | 11 → 11 | 9 → 9 | 20 → 20 | 0 → 0 | 6.648459 → 6.648459 |
| 372594943438356480 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0 → 0 | 0.000000 → 0.000000 |
| 373303811579576320 | 20 → 20 | 16 → 17 | 4 → 3 | 0 → 0 | 20 → 20 | 0.071363 → 0.068114 |

Private raw evidence remains local and is not committed. Only aggregate counts and Agent IDs are included here. The fixed cohort mapping, before/after executions, provider results and a SHA-256 manifest are retained for review.

Reproduce with `slop-discovery-replay -mode slop-replay -dir <private-directory>` and `python3 scripts/discovery_fidelity_replay/report.py --dir <private-directory> --variant slop`. Use frozen rules/model/rerank files and the documented read-only capture environment. Do not point evaluation or scratch writes at production.
