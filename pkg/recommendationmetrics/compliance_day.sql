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
    SELECT r.id FROM match_keys k CROSS JOIN LATERAL (
      -- Keep exact-key attribution as an indexed lookup. Otherwise estimates
      -- for the materialized key set can select a scan of all replay history.
      -- Impression is the selective leading key. Apply the other identity
      -- checks after lookup, rather than bitmap-scanning an Agent's history.
      SELECT r.id,r.agent_id,r.item_id,r.delivered,r.source_kind FROM replay_logs r
      WHERE r.impression_id=k.impression_id
      OFFSET 0
    ) r WHERE r.agent_id=k.agent_id AND r.item_id=k.item_id
      AND r.delivered IS TRUE AND r.source_kind='broadcast'
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
), delivery_counts AS MATERIALIZED (
    SELECT d.agent_id,count(*) AS delivered_exposures,
      count(*) FILTER (WHERE m.matches=1) AS identifiable_exposures
    FROM day_deliveries d LEFT JOIN exact_matches m USING(agent_id,item_id,impression_id)
    GROUP BY d.agent_id
), mature_keys AS MATERIALIZED (
    SELECT m.* FROM exact_matches m, bounds b
    WHERE m.matches=1 AND m.basis <> 'search' AND m.served_at >= b.lo
      AND m.served_at < b.hi AND m.served_at+172800000 <= b.cutoff
), outcomes AS MATERIALIZED (
    SELECT m.agent_id,m.impression_id,s.*,f.*,p.*
    FROM mature_keys m CROSS JOIN LATERAL (
      SELECT bool_or(f.feedback_at<m.served_at+3600000) AS scored_1h,
        bool_or(f.feedback_at<m.served_at+86400000) AS scored_24h,
        (array_agg(f.score ORDER BY f.feedback_at DESC,f.id DESC))[1] AS final_score
      FROM feedback_logs f
      WHERE f.impression_id=m.impression_id AND f.agent_id=m.agent_id AND f.item_id=m.item_id
        AND f.feedback_at>=m.served_at AND f.feedback_at<m.served_at+172800000
        AND f.score IN (-1,0,1,2)
    ) s CROSS JOIN LATERAL (
      SELECT bool_or(f.kind='surface') AS surfaced,
        bool_or(f.kind IN ('question','discussion','task')) AS followed_up
      FROM followup_labels f
      WHERE f.impression_id=m.impression_id AND f.impression_id<>''
        AND f.agent_id=m.agent_id AND f.item_id=m.item_id
        AND f.reported_at>=m.served_at AND f.reported_at<m.served_at+172800000
    ) f CROSS JOIN LATERAL (
      SELECT bool_or(msg.first_consumer_at>=m.served_at AND msg.first_consumer_at<m.served_at+172800000) AS agent_pm,
        bool_or(msg.first_consumer_at>=m.served_at AND msg.first_consumer_at<m.served_at+172800000
          AND msg.author_replied) AS author_replied
      FROM LATERAL (
        -- origin_id is the leading key of the existing partial index.
        SELECT c.conv_id,c.initiator_id,c.origin_type,c.participant_a,c.participant_b
        FROM conversations c WHERE c.origin_id=m.item_id AND c.origin_id>0 OFFSET 0
      ) c CROSS JOIN LATERAL (
        SELECT min(pm.created_at) FILTER (WHERE pm.sender_id=m.agent_id) AS first_consumer_at,
          bool_or(pm.sender_id=CASE WHEN c.participant_a=m.agent_id THEN c.participant_b ELSE c.participant_a END
            AND pm.created_at>=m.served_at AND pm.created_at<m.served_at+172800000) AS author_replied
        FROM private_messages pm WHERE pm.conv_id=c.conv_id
      ) msg
      WHERE c.origin_type='broadcast' AND c.initiator_id=m.agent_id
        AND m.agent_id IN (c.participant_a,c.participant_b)
    ) p
), batches AS (
    SELECT agent_id,impression_id,count(*) AS exposures,count(final_score) AS scored,
      min(final_score)=max(final_score) AS degenerate,
      bool_or(final_score>=1) AS positive,bool_or(surfaced) AS surfaced
    FROM outcomes GROUP BY agent_id,impression_id
), cohorts AS MATERIALIZED (
    SELECT c.agent_id,v.dimension,v.value
    FROM consumers c LEFT JOIN agent_settings s USING(agent_id)
    CROSS JOIN LATERAL (VALUES
      ('all','all'),
      ('runtime_name',COALESCE(NULLIF(left(lower(btrim(s.runtime_name)),64),''),'unknown')),
      ('runtime_mode',CASE WHEN s.mode IN ('plugin','skill') THEN s.mode ELSE 'unknown' END),
      ('cli_version',COALESCE(NULLIF(left(btrim(s.cli_version),64),''),'unknown')),
      ('model',COALESCE(NULLIF(left(lower(btrim(s.model)),64),''),'unknown')),
      ('skill_revision',COALESCE(NULLIF(left(s.skill_revision,64),''),'unknown')),
      ('heartbeat_contract_version',COALESCE(NULLIF(left(s.heartbeat_contract_version,64),''),'unknown')),
      ('auto_comment',COALESCE(s.auto_comment::text,'unknown'))
    ) v(dimension,value)
), kept_values AS MATERIALIZED (
    SELECT c.dimension,c.value FROM cohorts c JOIN delivery_counts d USING(agent_id)
    GROUP BY c.dimension,c.value HAVING count(*)>=5
), exposure_counts AS (
    SELECT agent_id,
      count(*) AS mature_exposures,
      count(*) FILTER (WHERE scored_1h) AS scored_1h_exposures,
      count(*) FILTER (WHERE scored_24h) AS scored_24h_exposures,
      count(final_score) AS scored_48h_exposures,
      count(*) FILTER (WHERE surfaced) AS surfaced_exposures,
      count(*) FILTER (WHERE surfaced AND final_score IS NULL) AS surfaced_unscored_exposures,
      count(*) FILTER (WHERE surfaced AND final_score<=0) AS surfaced_nonpositive_exposures,
      count(*) FILTER (WHERE final_score>=1) AS positive_exposures,
      count(*) FILTER (WHERE final_score>=1 AND surfaced) AS positive_surfaced_exposures,
      count(*) FILTER (WHERE followed_up) AS followup_exposures,
      count(*) FILTER (WHERE followed_up AND agent_pm) AS followup_agent_pm_exposures,
      count(*) FILTER (WHERE agent_pm) AS agent_pm_exposures,
      count(*) FILTER (WHERE author_replied) AS author_replied_exposures
    FROM outcomes GROUP BY agent_id
), batch_counts AS (
    SELECT agent_id,
      count(*) AS mature_batches,
      count(*) FILTER (WHERE scored=exposures) AS fully_scored_batches,
      count(*) FILTER (WHERE scored>0 AND scored<exposures) AS partially_scored_batches,
      count(*) FILTER (WHERE scored=0) AS unscored_batches,
      count(*) FILTER (WHERE scored>=5) AS degenerate_candidate_batches,
      count(*) FILTER (WHERE scored>=5 AND degenerate) AS degenerate_batches,
      count(*) FILTER (WHERE positive) AS positive_batches,
      count(*) FILTER (WHERE positive AND NOT COALESCE(surfaced,false)) AS positive_batches_without_surface
    FROM batches GROUP BY agent_id
), feedback_counts AS (
    SELECT agent_id,
      count(*) AS feedback_events,
      count(*) FILTER (WHERE f.impression_id<>'') AS feedback_events_with_impression,
      count(*) FILTER (WHERE m.matches=1 AND m.served_at<=f.feedback_at) AS feedback_events_exact
    FROM feedback f LEFT JOIN exact_matches m USING(agent_id,item_id,impression_id) GROUP BY agent_id
), surface_counts AS (
    SELECT agent_id,
      count(*) AS surface_events,
      count(*) FILTER (WHERE f.impression_id<>'') AS surface_events_with_impression,
      count(*) FILTER (WHERE m.matches=1 AND m.served_at<=f.reported_at) AS surface_events_exact
    FROM followups f LEFT JOIN exact_matches m USING(agent_id,item_id,impression_id) WHERE f.kind='surface' GROUP BY agent_id
), active_agents AS (
    SELECT agent_id FROM delivery_counts
    UNION SELECT agent_id FROM exposure_counts
    UNION SELECT agent_id FROM batch_counts
    UNION SELECT agent_id FROM feedback_counts
    UNION SELECT agent_id FROM surface_counts
), folded_counts AS (
    SELECT c.dimension,CASE WHEN c.dimension='all' OR k.value IS NOT NULL THEN c.value ELSE 'other' END AS value,
      sum(COALESCE(d.delivered_exposures,0)) AS delivered_exposures,
      count(d.agent_id) AS delivered_agents,
      sum(COALESCE(d.identifiable_exposures,0)) AS identifiable_exposures,
      sum(COALESCE(e.mature_exposures,0)) AS mature_exposures,
      sum(COALESCE(e.scored_1h_exposures,0)) AS scored_1h_exposures,
      sum(COALESCE(e.scored_24h_exposures,0)) AS scored_24h_exposures,
      sum(COALESCE(e.scored_48h_exposures,0)) AS scored_48h_exposures,
      sum(COALESCE(b.mature_batches,0)) AS mature_batches,
      sum(COALESCE(b.fully_scored_batches,0)) AS fully_scored_batches,
      sum(COALESCE(b.partially_scored_batches,0)) AS partially_scored_batches,
      sum(COALESCE(b.unscored_batches,0)) AS unscored_batches,
      sum(COALESCE(b.degenerate_candidate_batches,0)) AS degenerate_candidate_batches,
      sum(COALESCE(b.degenerate_batches,0)) AS degenerate_batches,
      sum(COALESCE(e.surfaced_exposures,0)) AS surfaced_exposures,
      sum(COALESCE(e.surfaced_unscored_exposures,0)) AS surfaced_unscored_exposures,
      sum(COALESCE(e.surfaced_nonpositive_exposures,0)) AS surfaced_nonpositive_exposures,
      sum(COALESCE(e.positive_exposures,0)) AS positive_exposures,
      sum(COALESCE(e.positive_surfaced_exposures,0)) AS positive_surfaced_exposures,
      sum(COALESCE(b.positive_batches,0)) AS positive_batches,
      sum(COALESCE(b.positive_batches_without_surface,0)) AS positive_batches_without_surface,
      sum(COALESCE(e.followup_exposures,0)) AS followup_exposures,
      sum(COALESCE(e.followup_agent_pm_exposures,0)) AS followup_agent_pm_exposures,
      sum(COALESCE(e.agent_pm_exposures,0)) AS agent_pm_exposures,
      sum(COALESCE(e.author_replied_exposures,0)) AS author_replied_exposures,
      sum(COALESCE(f.feedback_events,0)) AS feedback_events,
      sum(COALESCE(f.feedback_events_with_impression,0)) AS feedback_events_with_impression,
      sum(COALESCE(f.feedback_events_exact,0)) AS feedback_events_exact,
      sum(COALESCE(s.surface_events,0)) AS surface_events,
      sum(COALESCE(s.surface_events_with_impression,0)) AS surface_events_with_impression,
      sum(COALESCE(s.surface_events_exact,0)) AS surface_events_exact
    FROM active_agents a JOIN cohorts c USING(agent_id)
    LEFT JOIN delivery_counts d USING(agent_id)
    LEFT JOIN exposure_counts e USING(agent_id)
    LEFT JOIN batch_counts b USING(agent_id)
    LEFT JOIN feedback_counts f USING(agent_id)
    LEFT JOIN surface_counts s USING(agent_id)
    LEFT JOIN kept_values k USING(dimension,value)
    GROUP BY c.dimension,CASE WHEN c.dimension='all' OR k.value IS NOT NULL THEN c.value ELSE 'other' END
), row_keys AS (
    SELECT dimension,value FROM folded_counts UNION SELECT 'all','all'
)
INSERT INTO feedback_compliance_daily (
    day,dimension,value,
    delivered_exposures,
    delivered_agents,
    identifiable_exposures,
    mature_exposures,
    scored_1h_exposures,
    scored_24h_exposures,
    scored_48h_exposures,
    mature_batches,
    fully_scored_batches,
    partially_scored_batches,
    unscored_batches,
    degenerate_candidate_batches,
    degenerate_batches,
    surfaced_exposures,
    surfaced_unscored_exposures,
    surfaced_nonpositive_exposures,
    positive_exposures,
    positive_surfaced_exposures,
    positive_batches,
    positive_batches_without_surface,
    followup_exposures,
    followup_agent_pm_exposures,
    agent_pm_exposures,
    author_replied_exposures,
    feedback_events,
    feedback_events_with_impression,
    feedback_events_exact,
    surface_events,
    surface_events_with_impression,
    surface_events_exact,snapshot_at,schema_version
)
SELECT b.day,k.dimension,k.value,
    COALESCE(f.delivered_exposures,0),
    COALESCE(f.delivered_agents,0),
    COALESCE(f.identifiable_exposures,0),
    COALESCE(f.mature_exposures,0),
    COALESCE(f.scored_1h_exposures,0),
    COALESCE(f.scored_24h_exposures,0),
    COALESCE(f.scored_48h_exposures,0),
    COALESCE(f.mature_batches,0),
    COALESCE(f.fully_scored_batches,0),
    COALESCE(f.partially_scored_batches,0),
    COALESCE(f.unscored_batches,0),
    COALESCE(f.degenerate_candidate_batches,0),
    COALESCE(f.degenerate_batches,0),
    COALESCE(f.surfaced_exposures,0),
    COALESCE(f.surfaced_unscored_exposures,0),
    COALESCE(f.surfaced_nonpositive_exposures,0),
    COALESCE(f.positive_exposures,0),
    COALESCE(f.positive_surfaced_exposures,0),
    COALESCE(f.positive_batches,0),
    COALESCE(f.positive_batches_without_surface,0),
    COALESCE(f.followup_exposures,0),
    COALESCE(f.followup_agent_pm_exposures,0),
    COALESCE(f.agent_pm_exposures,0),
    COALESCE(f.author_replied_exposures,0),
    COALESCE(f.feedback_events,0),
    COALESCE(f.feedback_events_with_impression,0),
    COALESCE(f.feedback_events_exact,0),
    COALESCE(f.surface_events,0),
    COALESCE(f.surface_events_with_impression,0),
    COALESCE(f.surface_events_exact,0),to_timestamp(b.cutoff/1000.0),1
FROM bounds b CROSS JOIN row_keys k LEFT JOIN folded_counts f USING(dimension,value);
