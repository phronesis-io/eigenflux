-- psql -X -v ON_ERROR_STOP=1 -v since='2026-11-03 00:00:00+08'
--   -v until='2026-11-04 00:00:00+08' -f scripts/diagnostics/broadcast_behavior_signals.sql
-- Read-only event export. Use a settled window after asynchronous ingestion.
-- Missing events are unknown, not negative labels. detail_fetch is inspection,
-- not a human click or surface. Only matched rows have usable replay provenance;
-- keep search/recommendation and server/Agent evidence separate during training.
WITH bounds AS (
    SELECT (extract(epoch FROM :'since'::timestamptz)*1000)::bigint AS lo,
           (extract(epoch FROM :'until'::timestamptz)*1000)::bigint AS hi
), events AS (
    SELECT 'followup:' || f.id AS event_id, f.agent_id, f.item_id,
           f.impression_id, f.kind, NULL::smallint AS score, f.reported_at AS event_at,
           CASE WHEN f.kind = 'detail_fetch' THEN 'server' ELSE 'agent_report' END AS evidence
    FROM followup_labels f, bounds b
    WHERE f.reported_at >= b.lo AND f.reported_at < b.hi
      AND f.kind IN ('detail_fetch', 'surface', 'question', 'discussion', 'task')
    UNION ALL
    SELECT 'feedback:' || f.id, f.agent_id, f.item_id, f.impression_id,
           'score', f.score, f.feedback_at, 'agent_report'
    FROM feedback_logs f, bounds b
    WHERE f.feedback_at >= b.lo AND f.feedback_at < b.hi AND f.score IN (-1, 0, 1, 2)
), attributed AS (
    SELECT e.*, m.*,
           CASE WHEN e.impression_id = '' THEN 'missing_impression'
                WHEN m.matches = 0 THEN 'unmatched'
                WHEN m.matches > 1 THEN 'ambiguous'
                WHEN m.served_at > e.event_at THEN 'before_exposure'
                ELSE 'matched' END AS attribution
    FROM events e LEFT JOIN LATERAL (
        SELECT count(*) AS matches, min(r.id) AS replay_id, min(r.served_at) AS served_at,
               min(r.request_mode) AS request_mode, min(r.pipeline_version) AS pipeline_version
        FROM replay_logs r
        WHERE e.impression_id <> '' AND r.impression_id = e.impression_id
          AND r.agent_id = e.agent_id AND r.item_id = e.item_id
          AND r.source_kind = 'broadcast' AND r.delivered IS TRUE
    ) m ON true
)
SELECT event_id, agent_id, item_id, impression_id, kind, score, event_at, evidence, attribution,
       CASE WHEN attribution = 'matched' THEN replay_id END AS replay_id,
       CASE WHEN attribution = 'matched' THEN served_at END AS served_at,
       CASE WHEN attribution = 'matched' THEN request_mode END AS request_mode,
       CASE WHEN attribution = 'matched' THEN pipeline_version END AS pipeline_version
FROM attributed ORDER BY event_at, event_id;
