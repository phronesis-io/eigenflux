-- $1 is a Shanghai DATE; $2 is the common observation cutoff in epoch ms.
WITH bounds AS NOT MATERIALIZED (
    SELECT $1::date AS day, $2::bigint AS cutoff,
      (extract(epoch FROM ($1::date::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint AS lo,
      (extract(epoch FROM (($1::date+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))*1000)::bigint AS hi
), consumers AS MATERIALIZED (
    SELECT agent_id FROM agents
    WHERE NOT COALESCE(is_official, false)
      AND lower(email) NOT LIKE '%@pgc.eigenflux.one'
      AND lower(email) NOT LIKE '%@bot.eigenflux.one'
), feedback AS MATERIALIZED (
    SELECT f.* FROM feedback_logs f JOIN consumers c USING (agent_id), bounds b
    WHERE f.feedback_at >= b.lo AND f.feedback_at < LEAST(b.hi,b.cutoff)
      AND f.score IN (-1,0,1,2)
), followups AS MATERIALIZED (
    SELECT f.agent_id,f.item_id,f.impression_id,f.reported_at,f.kind
    FROM followup_labels f JOIN consumers c USING (agent_id), bounds b
    WHERE f.reported_at >= b.lo AND f.reported_at < LEAST(b.hi,b.cutoff)
      AND f.kind IN ('surface','question','discussion','task')
), event_keys AS MATERIALIZED (
    SELECT agent_id,item_id,impression_id FROM feedback WHERE impression_id <> ''
    UNION SELECT agent_id,item_id,impression_id FROM followups WHERE impression_id <> ''
), day_ids AS MATERIALIZED (
    SELECT r.id FROM replay_logs r JOIN consumers c USING (agent_id), bounds b
    WHERE r.served_at >= b.lo AND r.served_at < LEAST(b.hi,b.cutoff)
      AND r.delivered IS TRUE AND r.source_kind = 'broadcast'
), match_keys AS MATERIALIZED (
    SELECT agent_id,item_id,impression_id FROM event_keys
    UNION SELECT r.agent_id,r.item_id,r.impression_id
    FROM day_ids d JOIN replay_logs r USING(id) WHERE r.impression_id <> ''
), replay_ids AS MATERIALIZED (
    SELECT id FROM day_ids
    UNION
    SELECT r.id FROM (SELECT DISTINCT impression_id FROM match_keys) k CROSS JOIN LATERAL (
      -- Keep exact-key attribution as an indexed lookup. Otherwise estimates
      -- for the materialized key set can select a scan of all replay history.
      -- Read each shared batch once, then check the full item/Agent identity.
      -- Impression stays the selective leading key instead of an Agent scan.
      SELECT r.id,r.agent_id,r.item_id,r.impression_id,r.delivered,r.source_kind FROM replay_logs r
      WHERE r.impression_id=k.impression_id
      OFFSET 0
    ) r JOIN match_keys m ON r.agent_id=m.agent_id AND r.item_id=m.item_id
      AND r.impression_id=m.impression_id
    WHERE r.delivered IS TRUE AND r.source_kind='broadcast'
), classified AS MATERIALIZED (
    SELECT r.agent_id,r.item_id,r.impression_id,r.served_at,
      CASE
        WHEN r.request_mode = 'search' OR r.item_features #>> '{search,context,input_origin}' = 'query' THEN 'search'
        WHEN r.pipeline_version = 'need_search_v1' AND r.sample_schema_version = 2 AND r.request_mode = 'recommendation' THEN
          CASE
            WHEN r.item_features #>> '{search,context,input_origin}' = 'agent_context' AND COALESCE(r.need_id,0)=0 THEN 'profile'
            WHEN r.item_features #>> '{search,context,input_origin}' = 'need_input' AND r.need_id > 0
              AND r.item_features #>> '{search,context,source_need_id}' = r.need_id::text THEN 'need'
            WHEN r.item_features #>> '{search,context,input_origin}' = 'baseline' THEN 'baseline'
            WHEN r.item_features #>> '{search,context,input_origin}' = 'friend' THEN 'friend'
            ELSE 'unknown'
          END
        ELSE 'unknown'
      END AS basis,
      CASE
        WHEN lower(a.email) LIKE '%@pgc.eigenflux.one' THEN 'pgc'
        WHEN COALESCE(a.is_official,false) OR lower(a.email) LIKE '%@bot.eigenflux.one' THEN 'official'
        WHEN a.agent_id IS NOT NULL THEN 'ugc'
        ELSE 'unknown'
      END AS lane
    FROM replay_ids ids JOIN replay_logs r USING (id)
    LEFT JOIN raw_items i USING (item_id)
    LEFT JOIN agents a ON a.agent_id=i.author_agent_id
), exact_matches AS MATERIALIZED (
    -- More than one delivered position for the same key is ambiguous, even
    -- if both positions have the same basis. Never multiply event rows.
    SELECT agent_id,item_id,impression_id,count(*) AS matches,
      min(served_at) AS served_at,min(basis) AS basis,min(lane) AS lane
    FROM classified WHERE impression_id <> '' GROUP BY agent_id,item_id,impression_id
), day_deliveries AS MATERIALIZED (
    SELECT r.* FROM classified r, bounds b WHERE r.served_at >= b.lo
      AND r.served_at < LEAST(b.hi,b.cutoff) AND r.basis <> 'search'
), deliveries AS (
    SELECT d.basis,d.lane,count(*) AS delivery_rows,
      count(DISTINCT d.item_id) AS delivery_items,count(DISTINCT d.agent_id) AS delivery_agents,
      count(*) FILTER (WHERE COALESCE(m.matches,0) <> 1) AS unidentifiable_deliveries
    FROM day_deliveries d LEFT JOIN exact_matches m USING(agent_id,item_id,impression_id)
    GROUP BY d.basis,d.lane
), attributed_feedback AS MATERIALIZED (
    SELECT f.agent_id,f.score,f.feedback_at,
      CASE WHEN m.matches=1 AND m.served_at<=f.feedback_at THEN m.basis ELSE 'unattributed' END AS basis,
      CASE WHEN m.matches=1 AND m.served_at<=f.feedback_at THEN m.lane
        WHEN lower(a.email) LIKE '%@pgc.eigenflux.one' THEN 'pgc'
        WHEN COALESCE(a.is_official,false) OR lower(a.email) LIKE '%@bot.eigenflux.one' THEN 'official'
        WHEN a.agent_id IS NOT NULL THEN 'ugc' ELSE 'unknown' END AS lane
    FROM feedback f LEFT JOIN exact_matches m USING(agent_id,item_id,impression_id)
    LEFT JOIN raw_items i USING(item_id) LEFT JOIN agents a ON a.agent_id=i.author_agent_id
), scores AS (
    SELECT basis,lane,count(*) AS feedback_events,count(DISTINCT agent_id) AS feedback_agents,
      count(*) FILTER (WHERE score=-1) AS score_neg1,
      count(*) FILTER (WHERE score=0) AS score_0,
      count(*) FILTER (WHERE score=1) AS score_1,
      count(*) FILTER (WHERE score=2) AS score_2
    FROM attributed_feedback GROUP BY basis,lane
), attributed_followups AS (
    SELECT f.kind,
      CASE WHEN m.matches=1 AND m.served_at<=f.reported_at THEN m.basis ELSE 'unattributed' END AS basis,
      CASE WHEN m.matches=1 AND m.served_at<=f.reported_at THEN m.lane
        WHEN lower(a.email) LIKE '%@pgc.eigenflux.one' THEN 'pgc'
        WHEN COALESCE(a.is_official,false) OR lower(a.email) LIKE '%@bot.eigenflux.one' THEN 'official'
        WHEN a.agent_id IS NOT NULL THEN 'ugc' ELSE 'unknown' END AS lane
    FROM followups f LEFT JOIN exact_matches m USING(agent_id,item_id,impression_id)
    LEFT JOIN raw_items i USING(item_id) LEFT JOIN agents a ON a.agent_id=i.author_agent_id
), actions AS (
    SELECT basis,lane,
      count(*) FILTER (WHERE kind='surface') AS surface_events,
      count(*) FILTER (WHERE kind='question') AS question_events,
      count(*) FILTER (WHERE kind='discussion') AS discussion_events,
      count(*) FILTER (WHERE kind='task') AS task_events
    FROM attributed_followups GROUP BY basis,lane
), mature_keys AS MATERIALIZED (
    SELECT m.* FROM exact_matches m, bounds b
    WHERE m.matches=1 AND m.basis <> 'search' AND m.served_at >= b.lo
      AND m.served_at < b.hi AND m.served_at+172800000 <= b.cutoff
), mature_results AS (
    SELECT m.basis,m.lane,
      count(*) AS mature_exposures,count(*) FILTER(WHERE s.events>0) AS mature_scored_exposures,
      sum(s.events) AS mature_feedback_events,sum(s.neg) AS mature_score_neg1,
      sum(s.zero) AS mature_score_0,sum(s.pos) AS mature_score_1,sum(s.strong) AS mature_score_2
    FROM mature_keys m CROSS JOIN LATERAL (
      SELECT count(*) AS events,count(*) FILTER(WHERE f.score=-1) AS neg,
        count(*) FILTER(WHERE f.score=0) AS zero,count(*) FILTER(WHERE f.score=1) AS pos,
        count(*) FILTER(WHERE f.score=2) AS strong
      FROM feedback_logs f
      WHERE f.agent_id=m.agent_id AND f.item_id=m.item_id AND f.impression_id=m.impression_id
        AND f.feedback_at>=m.served_at AND f.feedback_at<m.served_at+172800000
        AND f.score IN(-1,0,1,2)
    ) s GROUP BY m.basis,m.lane
), dimensions AS (
    SELECT basis,lane FROM unnest(ARRAY['profile','need','baseline','friend','unknown','unattributed','search']) basis
      CROSS JOIN unnest(ARRAY['pgc','ugc','official','unknown']) lane
), hourly_deliveries AS (
    SELECT to_timestamp((served_at/3600000)*3600) AS hour_start,basis,lane,count(*) AS delivery_rows
    FROM day_deliveries GROUP BY hour_start,basis,lane
), hourly_scores AS (
    SELECT to_timestamp((feedback_at/3600000)*3600) AS hour_start,basis,lane,
      count(*) AS feedback_events,count(*) FILTER(WHERE score=-1) AS score_neg1,
      count(*) FILTER(WHERE score=0) AS score_0,count(*) FILTER(WHERE score=1) AS score_1,
      count(*) FILTER(WHERE score=2) AS score_2
    FROM attributed_feedback GROUP BY hour_start,basis,lane
), hours AS (
    SELECT to_timestamp((b.lo+n*3600000)/1000.0) AS hour_start,b.cutoff
    FROM bounds b CROSS JOIN generate_series(0,23) n WHERE b.lo+n*3600000<b.cutoff
), hourly_write AS (
    INSERT INTO recommendation_effect_hourly
    SELECT h.hour_start,g.basis,g.lane,COALESCE(d.delivery_rows,0),
      COALESCE(s.feedback_events,0),COALESCE(s.score_neg1,0),COALESCE(s.score_0,0),
      COALESCE(s.score_1,0),COALESCE(s.score_2,0),to_timestamp(h.cutoff/1000.0),1
    FROM dimensions g CROSS JOIN hours h
    LEFT JOIN hourly_deliveries d USING(hour_start,basis,lane)
    LEFT JOIN hourly_scores s USING(hour_start,basis,lane)
    ON CONFLICT(hour_start,basis,lane) DO UPDATE SET
      delivery_rows=EXCLUDED.delivery_rows,feedback_events=EXCLUDED.feedback_events,
      score_neg1=EXCLUDED.score_neg1,score_0=EXCLUDED.score_0,score_1=EXCLUDED.score_1,
      score_2=EXCLUDED.score_2,snapshot_at=EXCLUDED.snapshot_at
    WHERE recommendation_effect_hourly.snapshot_at<=EXCLUDED.snapshot_at
    RETURNING hour_start
)

INSERT INTO recommendation_effect_daily
SELECT b.day,g.basis,g.lane,
    COALESCE(d.delivery_rows,0),COALESCE(d.delivery_items,0),COALESCE(d.delivery_agents,0),COALESCE(d.unidentifiable_deliveries,0),
    COALESCE(s.feedback_events,0),COALESCE(s.feedback_agents,0),COALESCE(s.score_neg1,0),COALESCE(s.score_0,0),COALESCE(s.score_1,0),COALESCE(s.score_2,0),
    COALESCE(a.surface_events,0),COALESCE(a.question_events,0),COALESCE(a.discussion_events,0),COALESCE(a.task_events,0),
    COALESCE(m.mature_exposures,0),COALESCE(m.mature_scored_exposures,0),COALESCE(m.mature_feedback_events,0),
    COALESCE(m.mature_score_neg1,0),COALESCE(m.mature_score_0,0),COALESCE(m.mature_score_1,0),COALESCE(m.mature_score_2,0),
    to_timestamp(b.cutoff/1000.0),1
FROM dimensions g CROSS JOIN bounds b
LEFT JOIN deliveries d USING(basis,lane) LEFT JOIN scores s USING(basis,lane)
LEFT JOIN actions a USING(basis,lane) LEFT JOIN mature_results m USING(basis,lane)
ON CONFLICT(day,basis,lane) DO UPDATE SET
    delivery_rows=EXCLUDED.delivery_rows,delivery_items=EXCLUDED.delivery_items,delivery_agents=EXCLUDED.delivery_agents,
    unidentifiable_deliveries=EXCLUDED.unidentifiable_deliveries,
    feedback_events=EXCLUDED.feedback_events,feedback_agents=EXCLUDED.feedback_agents,
    score_neg1=EXCLUDED.score_neg1,score_0=EXCLUDED.score_0,score_1=EXCLUDED.score_1,score_2=EXCLUDED.score_2,
    surface_events=EXCLUDED.surface_events,question_events=EXCLUDED.question_events,
    discussion_events=EXCLUDED.discussion_events,task_events=EXCLUDED.task_events,
    mature_exposures=EXCLUDED.mature_exposures,mature_scored_exposures=EXCLUDED.mature_scored_exposures,
    mature_feedback_events=EXCLUDED.mature_feedback_events,mature_score_neg1=EXCLUDED.mature_score_neg1,
    mature_score_0=EXCLUDED.mature_score_0,mature_score_1=EXCLUDED.mature_score_1,mature_score_2=EXCLUDED.mature_score_2,
    snapshot_at=EXCLUDED.snapshot_at
WHERE recommendation_effect_daily.snapshot_at <= EXCLUDED.snapshot_at;
