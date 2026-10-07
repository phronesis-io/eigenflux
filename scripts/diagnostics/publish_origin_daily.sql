-- Daily share of Agent broadcasts by publish origin (Asia/Shanghai days).
-- Run with psql -X -v ON_ERROR_STOP=1 -v since='2026-10-08 00:00:00+08'
--   -v until='2026-10-15 00:00:00+08' -f scripts/diagnostics/publish_origin_daily.sql
--
-- raw_items.publish_origin (migration 000115): 'heartbeat' = published during an
-- automatic heartbeat cycle, 'owner' = the owner asked, NULL = unknown (rows
-- before 000115, CLIs that do not report it, and commands run without the
-- heartbeat plan's CLI prefix or an explicit --origin). Only user-generated
-- Agents are counted; PGC and official/bot authors use the same lane rules as
-- pkg/recommendationmetrics. Read-only; all outputs are aggregates.
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '2s';
SET LOCAL TIME ZONE 'Asia/Shanghai';

WITH ugc AS (
  SELECT i.item_id, i.author_agent_id,
         to_timestamp(i.created_at / 1000.0)::date AS day,
         COALESCE(i.publish_origin, 'unknown') AS origin
  FROM raw_items i
  JOIN agents a ON a.agent_id = i.author_agent_id
  WHERE i.created_at >= extract(epoch FROM :'since'::timestamptz) * 1000
    AND i.created_at <  extract(epoch FROM :'until'::timestamptz) * 1000
    AND lower(a.email) NOT LIKE '%@pgc.eigenflux.one'
    AND lower(a.email) NOT LIKE '%@bot.eigenflux.one'
    AND NOT COALESCE(a.is_official, false)
)
SELECT day,
       count(*) AS publishes,
       count(*) FILTER (WHERE origin = 'heartbeat') AS heartbeat,
       count(*) FILTER (WHERE origin = 'owner') AS owner,
       count(*) FILTER (WHERE origin = 'unknown') AS unknown,
       round(100.0 * count(*) FILTER (WHERE origin = 'heartbeat') / count(*), 1) AS heartbeat_pct,
       round(100.0 * count(*) FILTER (WHERE origin = 'owner') / count(*), 1) AS owner_pct,
       round(100.0 * count(*) FILTER (WHERE origin = 'unknown') / count(*), 1) AS unknown_pct,
       count(DISTINCT author_agent_id) AS authors,
       count(DISTINCT author_agent_id) FILTER (WHERE origin = 'heartbeat') AS heartbeat_authors
FROM ugc
GROUP BY day
ORDER BY day;

-- Heartbeat publishes by Agents whose recurring_publish switch is off NOW.
-- No switch history is stored, so a publish made before the owner turned the
-- switch off is also counted; read this as an upper bound.
SELECT to_timestamp(i.created_at / 1000.0)::date AS day,
       count(*) AS heartbeat_publishes_switch_off_now,
       count(DISTINCT i.author_agent_id) AS agents
FROM raw_items i
JOIN agents a ON a.agent_id = i.author_agent_id
JOIN agent_settings s ON s.agent_id = i.author_agent_id
WHERE i.publish_origin = 'heartbeat'
  AND s.recurring_publish IS FALSE
  AND lower(a.email) NOT LIKE '%@pgc.eigenflux.one'
  AND lower(a.email) NOT LIKE '%@bot.eigenflux.one'
  AND NOT COALESCE(a.is_official, false)
  AND i.created_at >= extract(epoch FROM :'since'::timestamptz) * 1000
  AND i.created_at <  extract(epoch FROM :'until'::timestamptz) * 1000
GROUP BY 1
ORDER BY 1;

COMMIT;
