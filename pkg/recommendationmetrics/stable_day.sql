WITH bounds AS MATERIALIZED (
 SELECT $1::date AS day,$2::bigint AS cutoff,
 (extract(epoch FROM (($1::date-7)::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint AS history_lo,
 (extract(epoch FROM ($1::date::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint AS lo,
 (extract(epoch FROM (($1::date+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint AS hi
), consumers AS MATERIALIZED (
 SELECT agent_id FROM agents
 WHERE NOT coalesce(is_official,false)
 AND lower(coalesce(email,'')) NOT LIKE '%@pgc.eigenflux.one'
 AND lower(coalesce(email,'')) NOT LIKE '%@bot.eigenflux.one'
), feedback AS MATERIALIZED (
 -- Per-Agent index range scans on (agent_id, feedback_at); no full feedback scan.
 SELECT c.agent_id,f.item_id,f.score,f.feedback_at>=b.lo AS on_day,
 timezone('Asia/Shanghai',to_timestamp(f.feedback_at/1000.0))::date AS event_day
 FROM bounds b CROSS JOIN consumers c CROSS JOIN LATERAL (
  SELECT item_id,score,feedback_at FROM feedback_logs f
  WHERE f.agent_id=c.agent_id AND f.feedback_at>=b.history_lo AND f.feedback_at<LEAST(b.hi,b.cutoff)
  AND f.score IN(-1,0,1,2) OFFSET 0) f
), stable AS MATERIALIZED (
 SELECT agent_id FROM feedback WHERE NOT on_day GROUP BY agent_id HAVING count(DISTINCT event_day)>=5
), day_feedback AS (
 SELECT f.agent_id,f.score,CASE
  WHEN lower(a.email) LIKE '%@pgc.eigenflux.one' THEN 'pgc'
  WHEN a.agent_id IS NOT NULL AND NOT coalesce(a.is_official,false)
   AND lower(coalesce(a.email,'')) NOT LIKE '%@bot.eigenflux.one' THEN 'ugc'
  ELSE 'other' END AS lane
 FROM feedback f JOIN stable s USING(agent_id)
 LEFT JOIN raw_items i ON i.item_id=f.item_id LEFT JOIN agents a ON a.agent_id=i.author_agent_id
 WHERE f.on_day
), result AS (
 SELECT b.day,(SELECT count(*) FROM stable) AS stable_agents,
 count(DISTINCT d.agent_id) AS scoring_agents,count(d.score) AS feedback_events,
 count(*) FILTER(WHERE d.score>=1) AS positive_events,
 count(*) FILTER(WHERE d.score>=1 AND d.lane='pgc') AS pgc_positive_events,
 count(*) FILTER(WHERE d.score>=1 AND d.lane='ugc') AS ugc_positive_events,
 count(*) FILTER(WHERE d.lane='pgc') AS pgc_feedback_events,
 count(*) FILTER(WHERE d.lane='ugc') AS ugc_feedback_events,
 to_timestamp(b.cutoff/1000.0) AS snapshot_at
 FROM bounds b LEFT JOIN day_feedback d ON true GROUP BY b.day,b.cutoff
)
INSERT INTO stable_agent_daily SELECT * FROM result
ON CONFLICT(day) DO UPDATE SET
 stable_agents=excluded.stable_agents,scoring_agents=excluded.scoring_agents,
 feedback_events=excluded.feedback_events,positive_events=excluded.positive_events,
 pgc_positive_events=excluded.pgc_positive_events,ugc_positive_events=excluded.ugc_positive_events,
 pgc_feedback_events=excluded.pgc_feedback_events,ugc_feedback_events=excluded.ugc_feedback_events,
 snapshot_at=excluded.snapshot_at
WHERE stable_agent_daily.snapshot_at<=excluded.snapshot_at;
