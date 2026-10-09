# Recommendation effect observations

Migration `000113` creates daily and hourly derived tables and read-only,
anonymous `grafana_recommendation_effect_daily` /
`grafana_recommendation_effect_hourly` views. It performs no historical
fact scan during deployment. Apply the migration before upgrading pipeline-cron.
Only the views are granted to `grafana_ro_v2`; private facts and context snapshots
remain inaccessible to that role. The matching independent Grafana page is
owned by `eigenflux-observability`.

## Classification and attribution

Observations cover delivered **broadcasts**, excluding official/internal
consumers (`is_official`, exact PGC and bot email domains). They do not change
ranking, recommendation weights, ingestion, or feedback storage.

- `profile`: schema-2 `need_search_v1` recommendation samples with the per-item
  `search.context.input_origin=agent_context` and no Need ID. This represents
  the Agent Card and compiled intent/context fallback, not deprecated profile
  extraction and not an API version.
- `need`: the same sample contract with `input_origin=need_input`, positive
  persisted `need_id`, and equal snapshot `source_need_id`.
- `baseline` and `friend` retain their explicit per-item origins. Legacy,
  malformed or unrecognized recommendation samples remain `unknown`.
- Search-mode or query-origin samples remain `search` and are excluded from
  recommendation delivery metrics. Exactly matched search feedback is counted
  separately for diagnosis.
- Author domains identify `pgc`; bot-domain or official authors become
  `official`; other known authors become `ugc`; missing authors are `unknown`.
  Official content is not silently included in UGC.

Feedback and follow-up reports require an exact nonempty
`(impression_id, agent_id, item_id)` match to **one delivered broadcast position**,
with event time at or after delivery. Duplicate matching positions, missing
samples and invalid sequencing become `unattributed`. No time-nearest join,
current Agent context, endpoint version or response-level origin is inferred.
A mixed batch can contain Profile and Need simultaneously. Known content class
is preserved even when the recommendation basis cannot be attributed.

## Units and maturity

Each daily row covers one `Asia/Shanghai` calendar day and one of seven fixed
bases and four fixed content classes. Delivery metrics use delivery time; feedback
and follow-up metrics use event time. Only scores `-1,0,1,2` are valid. Positive
events are `1+2`, strong positive events are `2`, and negative events are `-1`;
all four scores contribute to the denominator. Empty denominators are unknown,
not zero percent. Feedback counts are events, not distinct useful content.

Daily distinct content and consumer counts must not be added to claim distinct
people/content over a longer period or across groups. Aggregate percentages
use summed numerators divided by summed denominators.

The separate mature cohort uses unique, unambiguous exposure identities that
have had 48 hours since delivery. Its outcome window is
`[served_at, served_at+48h)`, identical for each exposure. Coverage counts mature
exposures with at least one valid score in that window over all identifiable
mature exposures; rate metrics still count score events in that window. A
late-ingested event within the window is picked up by subsequent refreshes
until the day is final (see below); later repairs need a manual backfill.
Scores at or after hour 48 do not alter that cohort. Empty/ambiguous identities
are excluded from coverage and reported as unidentifiable delivery rows.

Hourly activity uses the same classified deliveries and attributed scores as
its daily counterpart, grouped by actual delivery/feedback timestamps. A
successful refresh writes all 28 category rows for each elapsed hour of the day
(including the current partial hour); future hours have no rows. An observed
hour without events has zero counts and an unknown rate. Daily and hourly
activity totals reconcile, and both grids commit in the same SQL transaction.
The hourly view contains only delivery and valid-score counts, not private
identities, content, or fixed-maturity cohort measures.

The compact Grafana page defaults to hourly activity over the last 24 hours,
including the partial current hour, and supports a daily chart switch. Today
and the current hour can still change. The existing daily mature cohort remains
available for separate evaluations; changing graph granularity does not create
additional feedback or establish causal impact.

For a **whole delivery day** to be mature, its snapshot cutoff must be at least
Shanghai midnight at `day+3`. A mature before/after evaluation should exclude the change day and require seven
completely mature, observed days on each side. That comparison is outside the
compact activity page. It does not establish causal impact; traffic, content mix, exposure volume and scoring
participation can change.

## Refresh, failure and rollback

Pipeline-cron immediately attempts a batch, then runs every 15 minutes. Each
batch refreshes today and yesterday (Shanghai). A past day is refreshed until it
is final: its snapshot cutoff has reached Shanghai midnight at `day+3`, so every
48-hour outcome window has closed. Normally the only historical work is the day
that crossed `day+3` at midnight, which the first batch after 00:00 Shanghai
finalizes. Final days are never re-refreshed. Between `day+2` 00:00 and that
finalization the day keeps its last "yesterday" snapshot, so its mature cohort
is incomplete, as it is for any day before `day+3`.

The same check catches up after missed runs or deploys: days from `day-3` back
through `day-30` with a missing/incomplete grid or a pre-final snapshot are
refreshed newest first, at most two per batch. Each day gets its own
20-second context, 15-second SQL timeout and transaction. The batch deadline is
three minutes, below the five-minute Redis lease. A transaction advisory lock
also serializes each day across connections, and older cutoffs cannot overwrite
newer rows. No date is marked observed until its daily 28-row grid and
elapsed-hour grids commit together. Day selection also detects missing historical hourly grids.
One failed day leaves its old snapshot intact, is retried by the next batch, and
does not prevent independent dates in the batch from progressing. Grafana shows missing/stale dates rather
than filling failed calculations with zero. Logs include day and error, never
private facts or context payloads.

Day bounds are inlined so PostgreSQL can use date-range estimates. Attribution
looks up requested nonempty impressions through the existing impression index,
then verifies the full Agent/item key and duplicate count. It does not
scan all replay history to resolve a day's keys. Production query plans and
bounded read-only timings must be checked before enabling a release; fixture
timings are not a production capacity guarantee.

Snapshots older than the 30-day catch-up window are retained but no longer
checked; their recorded cutoff remains visible. To refresh an explicit day
manually (for example a gap older than 30 days, or after a source-data repair),
run the bounded backfill tool with the production environment; it uses the same
per-day transaction, timeouts and advisory lock as the cron:

```bash
go build -o build/recommendation_effect_backfill ./scripts/recommendation_effect_backfill/
./build/recommendation_effect_backfill --days=2026-09-01,2026-09-02
```
 Source replay/feedback retention can
limit historical reconstruction; unknown or missing samples must not be
reclassified as Profile. No unbounded historical backfill is run. If a bounded
day repeatedly exceeds its deadline, review its query plan before raising
limits or declaring the dashboard complete.

Code rollback can leave this additive schema in place. To remove the schema,
first stop the new cron and remove the Grafana page; Down drops only these
derived observations, leaving delivery/feedback/follow-up facts unchanged.

## Verification

Use only an isolated loopback PostgreSQL database named
`recommendation_metrics_*`:

```bash
RECOMMENDATION_METRICS_TEST_DSN="$LOCAL_METRICS_TEST_DSN" \
  go test -race -count=1 ./pkg/recommendationmetrics ./pipeline/cron \
  -run 'TestPostgresRecommendation|TestRecommendationEffect'
```

The native database fixtures verify mixed-batch attribution, typed-source
isolation, duplicate and undelivered samples, score denominators, internal
consumer exclusion, hour boundaries, current/future hours, daily/hourly
reconciliation, Shanghai midnight, strict maturity, repeats, stale writers,
interrupted transactions and a competing connection's advisory lock. Ordinary
Go tests without the explicit database setting skip the PostgreSQL cases.

## Long-term feedback history and volume diagnosis

Migration `000116` adds `feedback_history_daily` and the aggregate-only
`grafana_feedback_history_daily` view. Apply it before pipeline-cron. This
separate series includes **all** valid score events, including internal Agents,
legacy and unattributed feedback; it does not claim recommendation attribution
or causality. Score events use Shanghai event dates and scores -1, 0, 1, 2.
No private identifiers are granted to Grafana. Existing strict four-way
classification, filtering and source facts are unchanged.

Daily distinct scoring Agents, the top three Agents' score-event contribution,
PGC/UGC score counts, delivered broadcast positions and recipient counts help
separate changes in volume, participation and composition. Delivery excludes
explicit search; it includes legacy Feed. The union of recipients and scorers
is labelled participation, **not full-site DAU**. Feedback and deliveries are
bucketed by their own event time; their ratio is not exposure-cohort coverage.
Delivery-dependent counts are NULL before the first complete available replay
day. A failed or absent summary is not zero. Daily distinct counts cannot be
summed to estimate distinct Agents across dates. Author classification reflects
currently available identity records; unrecognized authors remain in totals.

The existing cron serially refreshes today and the previous two days, plus two
missing or unfinished older dates per batch, back to the earliest retained
feedback. A day finalizes after day+3. Each date has a 20-second deadline,
15-second statement timeout and a separate advisory transaction lock (116).
Stale cutoffs cannot overwrite newer snapshots, and failure preserves the last
committed row. Aggregates are retained indefinitely, independently of the
30-day strict-attribution catch-up window. Source deletion or late repair after
finalization requires a deliberate backfill; missing replay history cannot be
reconstructed. Backfill explicit dates serially without increasing timeouts:

```bash
go build -o build/recommendation_effect_backfill ./scripts/recommendation_effect_backfill/
./build/recommendation_effect_backfill --history --days=2026-04-13,2026-04-14
```

The dashboard shows complete daily actual rates beside seven-day ratios of
summed numerators/denominators. A missing day breaks the rolling window. Today
is explicitly excluded from the long-term complete-day panels. Current-day
activity remains available in the separate strict hourly diagnostic section.
Historical finalized snapshots do not trigger the live freshness threshold.
Rollback can leave the additive history table/view in place; remove dashboard
consumers and stop the new writer before applying Down, which deletes only the
derived history, never the original feedback or replay data.
