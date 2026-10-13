-- Run with psql -X -v ON_ERROR_STOP=1 -v since='2026-10-02 14:07:00+08'
--   -v until='2026-10-04 14:30:00+08' -f scripts/diagnostics/discovery_attribution.sql
-- Leave ingestion time (at least two minutes) before until. Never infer missing
-- impression IDs from timestamp proximity. All outputs are aggregates.
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '2s';
SET LOCAL TIME ZONE 'Asia/Shanghai';

SELECT now() AS audited_at, :'since'::timestamptz AS window_start,
       :'until'::timestamptz AS window_end;

SELECT pipeline_version, request_mode, source_kind, count(*) AS exposures,
       count(*) FILTER (WHERE delivered IS NOT TRUE) AS not_delivered,
       count(*) FILTER (WHERE pipeline_version = 'need_search_v1' AND
         (context_id IS NULL OR sample_schema_version <> 2 OR source_id IS NULL)) AS invalid_snapshot,
       count(*) FILTER (WHERE item_features->'search'->'lr' IS NOT NULL) AS lr_scored
FROM replay_logs
WHERE served_at >= extract(epoch FROM :'since'::timestamptz)*1000
  AND served_at < extract(epoch FROM :'until'::timestamptz)*1000
GROUP BY 1,2,3 ORDER BY 1,2,3;

WITH events AS (
  SELECT 'score' AS event_type, agent_id, item_id, impression_id, feedback_at AS ts
  FROM feedback_logs
  WHERE feedback_at >= extract(epoch FROM :'since'::timestamptz)*1000
    AND feedback_at < extract(epoch FROM :'until'::timestamptz)*1000
  UNION ALL
  SELECT kind, agent_id, item_id, impression_id, reported_at FROM followup_labels
  WHERE reported_at >= extract(epoch FROM :'since'::timestamptz)*1000
    AND reported_at < extract(epoch FROM :'until'::timestamptz)*1000
), matches AS (
  SELECT e.*, r.pipeline_version, r.request_mode, r.served_at, r.delivered
  FROM events e LEFT JOIN LATERAL (
    SELECT pipeline_version, request_mode, served_at, delivered FROM replay_logs r
    WHERE r.impression_id=e.impression_id AND r.agent_id=e.agent_id AND r.item_id=e.item_id
      AND r.source_kind='broadcast'
    ORDER BY served_at LIMIT 1
  ) r ON true
)
SELECT event_type, count(*) AS total,
       count(*) FILTER (WHERE impression_id='') AS missing_impression,
       count(*) FILTER (WHERE impression_id<>'' AND served_at IS NULL) AS unmatched,
       count(*) FILTER (WHERE pipeline_version='need_search_v1' AND request_mode='recommendation') AS discovery_recommendation,
       count(*) FILTER (WHERE pipeline_version='need_search_v1' AND request_mode='search') AS discovery_search,
       count(*) FILTER (WHERE pipeline_version='legacy_feed_v1') AS legacy,
       count(*) FILTER (WHERE ts<served_at) AS before_exposure,
       count(*) FILTER (WHERE served_at IS NOT NULL AND delivered IS NOT TRUE) AS not_delivered
FROM matches GROUP BY 1 ORDER BY 1;

-- These are unique labeled exposures, not the number of label events. Search
-- positives remain observable but are outside the discovery LR training set.
SELECT request_mode, count(*) AS broadcast_exposures,
       count(*) FILTER (WHERE EXISTS (
         SELECT 1 FROM followup_labels f
         WHERE f.impression_id=r.impression_id AND f.agent_id=r.agent_id AND f.item_id=r.item_id
           AND f.kind IN ('surface','question','discussion','task')
           AND f.reported_at>=r.served_at
           AND f.reported_at<extract(epoch FROM :'until'::timestamptz)*1000
       )) AS labeled_exposures
FROM replay_logs r
WHERE pipeline_version='need_search_v1' AND source_kind='broadcast' AND delivered IS TRUE
  AND served_at>=extract(epoch FROM :'since'::timestamptz)*1000
  AND served_at<extract(epoch FROM :'until'::timestamptz)*1000
GROUP BY 1 ORDER BY 1;

-- A lower bound on missing consumption, not a historical reconciliation delta:
-- item_stats is cumulative and may already contain legacy exposure counts.
WITH delivered AS (
  SELECT item_id, count(*) AS n FROM replay_logs
  WHERE pipeline_version='need_search_v1' AND source_kind='broadcast' AND delivered IS TRUE
    AND served_at>=extract(epoch FROM :'since'::timestamptz)*1000
    AND served_at<extract(epoch FROM :'until'::timestamptz)*1000
  GROUP BY item_id
)
SELECT count(*) AS distinct_items, sum(n) AS exposures,
       count(*) FILTER (WHERE coalesce(s.consumed_count,0)=0) AS zero_consumed_items,
       sum(n) FILTER (WHERE coalesce(s.consumed_count,0)=0) AS zero_consumed_exposures
FROM delivered d LEFT JOIN item_stats s USING(item_id);
COMMIT;
