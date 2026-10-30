# UGC AI Slop V0.2.1

`UGC_SLOP_ENABLED=false` (default) retains the existing extraction path. Enabling
it selects `process_item_ugc` using the publisher's authoritative account and the
existing PGC suffix policy. PGC retains its existing template and summary path.
`UGC_SLOP_MODEL` overrides the UGC extraction model; empty uses `LLM_MODEL`.
The release target for this UGC path is `qwen3.8-flash`. The owner authorized
Slop first; global main/safety/translation/PGC model changes are deferred.
`UGC_SLOP_ROLLOUT_BPS` defaults to 10000 but the first activation is 1000 (10%
of stable publisher hash space). Zero or out-of-range shares admit no publishers.
Every worker and retry uses the same publisher cohort; traffic share need not be
exactly 10%. The enable switch still defaults to false.

The UGC template combines the unchanged current extraction rules with the exact
V0.2.1 classification module. It does not overwrite the current template with the
handoff's older full-template snapshot. The call uses the existing Responses API,
a 60-second context timeout, existing bounded retries, temperature=0 and explicit
reasoning.effort=none. The latter is required for Qwen 3.8, where omitting reasoning
defaults to xhigh. The handoff used Chat Completions with enable_thinking=false;
the frozen development benchmark is still not a production guarantee.

For a non-discard UGC result, `is_slop` must be integer 0 or 1, `slop_score` and
quality must be finite [0,1], and the reason must be nonempty English. Structural
validation rejects missing values, booleans, null and out-of-range values; English
validation rejects CJK scripts but is not a general language detector. Original
short discard results require no Slop fields. `slop_score` never controls routing.

Migration 000124 must precede Pipeline deployment. A per-item evaluation saves the
validated response, original/effective quality, model, prompt version, input hash,
rendered-template hash, request hash and evaluation time. The request hash binds
the rendered input, model, max tokens, temperature and reasoning contract; it excludes secrets.
History remains absent/unevaluated. Concurrent results use the first committed
checkpoint. A Slop checkpoint and discarded status commit atomically. It keeps the
existing public `content_evaluation` category; detailed reasons remain private.

Slop is blocked independently of the quality threshold, even at threshold zero
and with homepage_eligible=true. Enabled UGC is not draft-indexed before evaluation.
After committing a block, index deletion is retried using the terminal state,
without another model call. Non-Slop retains the original quality threshold.
Original content and the decision remain stored. This release does not re-evaluate
historical completed broadcasts or add a user-facing rejection reason.

## Activation and rollback

Deploy with the switch off, then activate only after the actual model/request
regression, recent-context delivery replay and PR review. Record model replacement
and Slop activation separately: replacing the main Qwen model also affects existing
extraction paths. Turn `UGC_SLOP_ENABLED=false` and restart/deploy Pipeline to restore
the original prompt and routing for new work. Merely ignoring `is_slop` while still
running the Slop prompt would not roll back its quality=0 instruction. Already
blocked items remain blocked and are not automatically republished. Retain the
migration on code rollback. Drain/replace all old Pipeline workers before activation;
old workers do not know the new gate.

## Evidence and monitoring

The handoff's 109 jointly scorable development samples exclude BC015 and differing
discard results BC028/BC030. V0.2.1 has TP19/FN41/FP2/TN47, accuracy 60.55%, recall
31.67%, precision 90.48%, ROC-AUC 0.7672. Baseline: accuracy 53.21%, recall 21.67%,
AUC 0.6468, FN47, FP4. BC109 and BC141 are acknowledged ambiguous cases; Pascal
judges them close to Slop. Keep both false positives in the original-label metrics:
2/49 non-Slop samples (4.08%), or 2/109 of all samples (1.83%). These denominators
are different. This repeatedly calibrated development set is not an unbiased
estimate of online error.

`/d/ugc-ai-slop` displays this frozen benchmark, hourly durable decisions and score
bins, model-quality overrides, retry-inclusive call outcomes, worker enablement
and existing recommendation effect panels. The read-only Grafana role receives
only anonymous aggregate views, not raw responses or reasons. Missing data is not
zero. Live accuracy/recall/AUC require new human labels.

The [2026-10-30 gate sensitivity](ugc-slop-replay-20261030.md) is complete; it
shows a 2.79% lower historical-rate positive-event estimate at full gate coverage.
The owner clarified that Agent approval can reward low-value Slop: this decrease
is diagnostic, not a release blocker or standalone rollback trigger. Human-reviewed
false positives, valuable-content retention and Slop leakage are the acceptance
criteria. Global model-rollout acceptance remains separate. Before merge, retain the complete fixed-cohort replay with PGC/UGC and Need/Profile
mix and each Agent's expected deliveries/positive events. After activation check
the next complete calendar day, comparing the same cohort's actual daily positives
and delivered mix against preceding complete days. Partial-day data and model
replacement must be identified separately. No effect claim follows from passing
latency or health checks alone.

Native transaction/migration regressions use `SLOP_PG_TEST_DSN`, a dedicated
loopback database whose name contains `slop_validation`. Run
`go test -race ./migrations ./rpc/item/dal -run '^TestSlop'` with that variable.
The ordinary service `PG_DSN` does not opt into these isolated tests.

## First activation

After required PR approval and a clean main deployment, set only
`UGC_SLOP_ENABLED=true`, `UGC_SLOP_MODEL=qwen3.8-flash` and
`UGC_SLOP_ROLLOUT_BPS=1000` in the root-managed runtime configuration and use the
approved deployer. Keep other model settings unchanged. Verify all Pipeline
workers report enablement 1 and rollout share 0.1, confirm aggregate views and
Grafana queries, and inspect organic new decisions without publishing synthetic
production content. Do not automatically expand the cohort.

Rollback is `UGC_SLOP_ENABLED=false` followed by the same managed rollout. Model
contract failures, incomplete cleanup or a confirmed valuable-content false
positive require investigation and can justify disabling the gate. Agent positive
feedback alone cannot establish usefulness or trigger rollback.

Check on the day after activation, identifying partial-day data. Review the first
complete calendar day once it closes. The fixed 10-Agent cohort remains the
delivery diagnostic; manual review of blocked and admitted UGC is separate and
must report sample sizes and unresolved labels.
