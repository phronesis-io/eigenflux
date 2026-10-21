WITH bounds AS MATERIALIZED (
 SELECT $1::date AS day,$2::bigint AS cutoff,
 (extract(epoch FROM ($1::date::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint AS lo,
 (extract(epoch FROM (($1::date+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint AS hi
), consumers AS MATERIALIZED (
 SELECT agent_id FROM agents
 WHERE NOT coalesce(is_official,false)
 AND lower(coalesce(email,'')) NOT LIKE '%@pgc.eigenflux.one'
 AND lower(coalesce(email,'')) NOT LIKE '%@bot.eigenflux.one'
), scored AS MATERIALIZED (
 -- One row per (Agent, broadcast) scored on the day; per-Agent index ranges, no full scan.
 SELECT c.agent_id,f.item_id,max(f.score) AS score,min(f.feedback_at) FILTER(WHERE f.score>=1) AS good_at
 FROM bounds b CROSS JOIN consumers c CROSS JOIN LATERAL (
  SELECT item_id,score,feedback_at FROM feedback_logs f
  WHERE f.agent_id=c.agent_id AND f.feedback_at>=b.lo AND f.feedback_at<LEAST(b.hi,b.cutoff)
  AND f.score IN(-1,0,1,2) OFFSET 0) f
 GROUP BY c.agent_id,f.item_id
), laned AS MATERIALIZED (
 SELECT s.*,i.author_agent_id,CASE
  WHEN lower(a.email) LIKE '%@pgc.eigenflux.one' THEN 'pgc'
  WHEN a.agent_id IS NOT NULL AND NOT coalesce(a.is_official,false)
   AND lower(coalesce(a.email,'')) NOT LIKE '%@bot.eigenflux.one' THEN 'ugc'
  ELSE 'other' END AS lane
 FROM scored s LEFT JOIN raw_items i ON i.item_id=s.item_id LEFT JOIN agents a ON a.agent_id=i.author_agent_id
), messaged AS MATERIALIZED (
 -- The scorer's broadcast-origin conversation with the author about this broadcast
 -- (unique per pair and broadcast), opened from 1 hour before to 24 hours after the
 -- first good score.
 SELECT c.conv_id,l.author_agent_id,m.sent_at
 FROM laned l CROSS JOIN bounds b
 JOIN conversations c ON c.origin_type='broadcast' AND c.origin_id=l.item_id
  AND c.participant_a=LEAST(l.agent_id,l.author_agent_id) AND c.participant_b=GREATEST(l.agent_id,l.author_agent_id)
 CROSS JOIN LATERAL (
  SELECT min(pm.created_at) AS sent_at FROM private_messages pm
  WHERE pm.conv_id=c.conv_id AND pm.sender_id=l.agent_id
  AND pm.created_at>=l.good_at-3600000 AND pm.created_at<=l.good_at+86400000 AND pm.created_at<b.cutoff) m
 WHERE l.lane='ugc' AND l.score>=1 AND l.author_agent_id<>l.agent_id AND m.sent_at IS NOT NULL
), result AS (
 SELECT b.day,
 (SELECT count(*) FROM laned) AS scored_pairs,
 (SELECT count(*) FROM laned WHERE score>=1) AS good_pairs,
 (SELECT count(*) FROM laned WHERE lane='pgc') AS pgc_scored_pairs,
 (SELECT count(*) FROM laned WHERE lane='pgc' AND score>=1) AS pgc_good_pairs,
 (SELECT count(*) FROM laned WHERE lane='ugc') AS ugc_scored_pairs,
 (SELECT count(*) FROM laned WHERE lane='ugc' AND score>=1) AS ugc_good_pairs,
 (SELECT count(*) FROM messaged) AS ugc_messaged_pairs,
 (SELECT count(*) FROM messaged m WHERE EXISTS (
  SELECT 1 FROM private_messages pm WHERE pm.conv_id=m.conv_id AND pm.sender_id=m.author_agent_id
  AND pm.created_at>m.sent_at AND pm.created_at<=m.sent_at+259200000 AND pm.created_at<b.cutoff)) AS ugc_mutual_pairs,
 to_timestamp(b.cutoff/1000.0) AS snapshot_at
 FROM bounds b
)
INSERT INTO match_overview_daily SELECT * FROM result
ON CONFLICT(day) DO UPDATE SET
 scored_pairs=excluded.scored_pairs,good_pairs=excluded.good_pairs,
 pgc_scored_pairs=excluded.pgc_scored_pairs,pgc_good_pairs=excluded.pgc_good_pairs,
 ugc_scored_pairs=excluded.ugc_scored_pairs,ugc_good_pairs=excluded.ugc_good_pairs,
 ugc_messaged_pairs=excluded.ugc_messaged_pairs,ugc_mutual_pairs=excluded.ugc_mutual_pairs,
 snapshot_at=excluded.snapshot_at
WHERE match_overview_daily.snapshot_at<=excluded.snapshot_at;
