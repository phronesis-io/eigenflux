WITH bounds AS NOT MATERIALIZED (
 SELECT $1::date AS day,$2::bigint cutoff,
 (extract(epoch FROM ($1::date::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint lo,
 (extract(epoch FROM (($1::date+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint hi
), feedback AS MATERIALIZED (
 SELECT f.agent_id,f.item_id,f.score FROM feedback_logs f,bounds b
 WHERE f.feedback_at>=b.lo AND f.feedback_at<LEAST(b.hi,b.cutoff) AND f.score IN(-1,0,1,2)
), agent_counts AS (
 SELECT agent_id,count(*) n FROM feedback GROUP BY agent_id
), scores AS (
 SELECT count(*) feedback_events,count(DISTINCT agent_id) feedback_agents,
 count(*) FILTER(WHERE score=-1) score_neg1,count(*) FILTER(WHERE score=0) score_0,
 count(*) FILTER(WHERE score=1) score_1,count(*) FILTER(WHERE score=2) score_2
 FROM feedback
), content AS (
 SELECT count(*) FILTER(WHERE lower(a.email) LIKE '%@pgc.eigenflux.one') pgc_feedback_events,
 count(*) FILTER(WHERE a.agent_id IS NOT NULL AND NOT coalesce(a.is_official,false)
  AND lower(a.email) NOT LIKE '%@pgc.eigenflux.one' AND lower(a.email) NOT LIKE '%@bot.eigenflux.one') ugc_feedback_events
 FROM feedback f LEFT JOIN raw_items i USING(item_id) LEFT JOIN agents a ON a.agent_id=i.author_agent_id
), replay_start AS (
 SELECT served_at FROM replay_logs ORDER BY served_at LIMIT 1
), delivered AS MATERIALIZED (
 SELECT r.agent_id FROM replay_logs r,bounds b
 WHERE r.served_at>=b.lo AND r.served_at<LEAST(b.hi,b.cutoff) AND r.delivered IS TRUE
 AND r.source_kind='broadcast' AND r.request_mode IS DISTINCT FROM 'search'
 AND (r.item_features #>> '{search,context,input_origin}') IS DISTINCT FROM 'query'
), result AS (
 SELECT b.day,s.*,coalesce((SELECT sum(n) FROM (SELECT n FROM agent_counts ORDER BY n DESC LIMIT 3)t),0) top3_feedback_events,
 c.*,CASE WHEN (SELECT served_at FROM replay_start)<=b.lo THEN (SELECT count(*) FROM delivered) END delivery_rows,
 CASE WHEN (SELECT served_at FROM replay_start)<=b.lo THEN (SELECT count(DISTINCT agent_id) FROM delivered) END delivery_agents,
 CASE WHEN (SELECT served_at FROM replay_start)<=b.lo THEN (SELECT count(*) FROM (SELECT agent_id FROM delivered UNION SELECT agent_id FROM feedback)t) END participating_agents,
 to_timestamp(b.cutoff/1000.0) snapshot_at FROM bounds b CROSS JOIN scores s CROSS JOIN content c
)
INSERT INTO feedback_history_daily SELECT * FROM result
ON CONFLICT(day) DO UPDATE SET
 feedback_events=excluded.feedback_events,feedback_agents=excluded.feedback_agents,
 score_neg1=excluded.score_neg1,score_0=excluded.score_0,score_1=excluded.score_1,score_2=excluded.score_2,
 top3_feedback_events=excluded.top3_feedback_events,pgc_feedback_events=excluded.pgc_feedback_events,
 ugc_feedback_events=excluded.ugc_feedback_events,
 delivery_rows=coalesce(excluded.delivery_rows,feedback_history_daily.delivery_rows),
 delivery_agents=coalesce(excluded.delivery_agents,feedback_history_daily.delivery_agents),
 participating_agents=coalesce(excluded.participating_agents,feedback_history_daily.participating_agents),
 snapshot_at=excluded.snapshot_at
WHERE feedback_history_daily.snapshot_at<=excluded.snapshot_at;
