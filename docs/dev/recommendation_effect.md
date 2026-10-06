# Recommendation effect observations

Migration `000113` creates `recommendation_effect_daily` and the read-only,
anonymous `grafana_recommendation_effect_daily` view. It performs no historical
fact scan during deployment. Apply the migration before upgrading pipeline-cron.
Only the view is granted to `grafana_ro_v2`; private facts and context snapshots
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

Each row covers one `Asia/Shanghai` calendar day and one of seven fixed bases
and four fixed content classes. Delivery metrics use delivery time; feedback
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
late-ingested event within the window is picked up by subsequent refreshes.
Scores at or after hour 48 do not alter that cohort. Empty/ambiguous identities
are excluded from coverage and reported as unidentifiable delivery rows.

For a **whole delivery day** to be mature, its snapshot cutoff must be at least
Shanghai midnight at `day+3`. Today is displayed separately. Before/after
comparison excludes the change day and requires seven completely mature,
observed days on each side before showing either side's percentages. It does
not establish causal impact; traffic, content mix, exposure volume and scoring
participation can change. A/A validates plumbing, not efficacy.

## Refresh, failure and rollback

Pipeline-cron immediately attempts a batch, then runs every five minutes. Each
batch selects today plus five oldest/missing days in the latest 31-day window;
the normal historical refresh cycle is about 30 minutes. Each day gets its own
20-second context, 15-second SQL timeout and transaction. The batch deadline is
three minutes, below the five-minute Redis lease. A transaction advisory lock
also serializes each day across connections, and older cutoffs cannot overwrite
newer rows. No date is marked observed until its entire 28-row grid commits.
One failed day leaves its old snapshot intact and does not prevent independent
dates in the batch from progressing. Grafana shows missing/stale dates rather
than filling failed calculations with zero. Logs include day and error, never
private facts or context payloads.

Snapshots outside the 31-day refresh window are retained but stop updating;
their recorded cutoff remains visible. Source replay/feedback retention can
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
consumer exclusion, Shanghai midnight, strict maturity, repeats, stale writers,
interrupted transactions and a competing connection's advisory lock. Ordinary
Go tests without the explicit database setting skip the PostgreSQL cases.
