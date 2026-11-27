-- Impact queries, run around 2026-11-27 17:40 (UTC+8) through the Grafana read-only datasource grafana_ro_v2.
-- Eligibility matches pkg/need.Store.Active: current_need_inputs, need_type=broadcast, deadline not passed.
-- 7-day window: 2026-11-20 00:00 to 2026-11-27 00:00 (UTC+8). Positive rating = feedback_logs.score >= 1.

-- [q1]
WITH elig AS (SELECT agent_id, intent_id, COALESCE((input->>'priority')::double precision,0) AS pri FROM current_need_inputs WHERE input->>'need_type'='broadcast' AND (input->'constraints'->>'deadline_ms' IS NULL OR (input->'constraints'->>'deadline_ms')::bigint > (extract(epoch from now())*1000)::bigint)),
per AS (SELECT agent_id, count(*) n, count(DISTINCT pri) pri_levels FROM elig GROUP BY 1)
SELECT count(*) agents, sum(n) needs,
  count(*) FILTER (WHERE n>5) agents_gt5, sum(n) FILTER (WHERE n>5) needs_of_gt5,
  sum(n-5) FILTER (WHERE n>5) needs_cut,
  round(100.0*sum(n-5) FILTER (WHERE n>5)/sum(n),1) pct_needs_cut,
  percentile_disc(0.5) WITHIN GROUP (ORDER BY n) p50, percentile_disc(0.9) WITHIN GROUP (ORDER BY n) p90,
  percentile_disc(0.99) WITHIN GROUP (ORDER BY n) p99, max(n) max_n,
  count(*) FILTER (WHERE n>5 AND pri_levels=1) gt5_single_priority, now() as_of
FROM per
;

-- [q2]
WITH per AS (SELECT agent_id, count(*) n FROM current_need_inputs WHERE input->>'need_type'='broadcast' AND (input->'constraints'->>'deadline_ms' IS NULL OR (input->'constraints'->>'deadline_ms')::bigint > (extract(epoch from now())*1000)::bigint) GROUP BY 1)
SELECT CASE WHEN n<=5 THEN '01-05' WHEN n<=10 THEN '06-10' WHEN n<=16 THEN '11-16' WHEN n<=30 THEN '17-30' ELSE '31+' END bucket,
  count(*) agents, sum(n) needs, sum(greatest(n-5,0)) cut FROM per GROUP BY 1 ORDER BY 1
;

-- [q3]
WITH elig AS (SELECT agent_id, intent_id,
    row_number() OVER (PARTITION BY agent_id ORDER BY COALESCE((input->>'priority')::double precision,0) DESC, created_at DESC, need_input_id ASC) pos FROM current_need_inputs WHERE input->>'need_type'='broadcast' AND (input->'constraints'->>'deadline_ms' IS NULL OR (input->'constraints'->>'deadline_ms')::bigint > (extract(epoch from now())*1000)::bigint)),
it AS (SELECT agent_id, intent_id, count(*) FILTER (WHERE pos<=5) sel FROM elig GROUP BY 1,2)
SELECT count(*) intents, count(*) FILTER (WHERE sel=0) intents_never_selected,
  count(DISTINCT agent_id) FILTER (WHERE sel=0) agents_with_dark_intent FROM it
;

-- [q4]
SELECT COALESCE(input->>'priority','(null)') pri, count(*) n, count(DISTINCT agent_id) agents FROM current_need_inputs WHERE input->>'need_type'='broadcast' GROUP BY 1 ORDER BY 2 DESC LIMIT 10
;

-- [q5]
SELECT input->>'need_type' kind, count(*) needs, count(DISTINCT agent_id) agents FROM current_need_inputs GROUP BY 1
;

-- [a0]
SELECT request_mode, source_kind, delivered, count(*) n, count(need_id) with_need FROM replay_logs r WHERE r.served_at >= (extract(epoch from timestamptz '2026-11-20 00:00+08')*1000)::bigint AND r.served_at < (extract(epoch from timestamptz '2026-11-27 00:00+08')*1000)::bigint GROUP BY 1,2,3 ORDER BY 4 DESC LIMIT 20
;

-- [a1]
WITH per AS (SELECT agent_id, count(*) n FROM current_need_inputs WHERE input->>'need_type'='broadcast' AND (input->'constraints'->>'deadline_ms' IS NULL OR (input->'constraints'->>'deadline_ms')::bigint > (extract(epoch from now())*1000)::bigint) GROUP BY 1),
d AS (SELECT r.agent_id, count(*) deliveries, count(r.need_id) need_deliveries, count(DISTINCT r.need_id) needs_hit
  FROM replay_logs r WHERE r.served_at >= (extract(epoch from timestamptz '2026-11-20 00:00+08')*1000)::bigint AND r.served_at < (extract(epoch from timestamptz '2026-11-27 00:00+08')*1000)::bigint AND r.request_mode='recommendation' AND r.source_kind='broadcast' GROUP BY 1)
SELECT CASE WHEN p.n IS NULL THEN 'a_no_need' WHEN p.n<=5 THEN 'b_1-5' ELSE 'c_6+' END grp,
  count(*) active_agents, sum(deliveries) deliveries, sum(need_deliveries) need_deliveries,
  round(100.0*sum(need_deliveries)/sum(deliveries),1) need_share_pct,
  sum(p.n) eligible_needs_now, sum(needs_hit) distinct_needs_delivered, sum(greatest(p.n-5,0)) needs_cut_now
FROM d LEFT JOIN per p USING (agent_id) GROUP BY 1 ORDER BY 1
;

-- [a2]
WITH per AS (SELECT agent_id, count(*) n FROM current_need_inputs WHERE input->>'need_type'='broadcast' AND (input->'constraints'->>'deadline_ms' IS NULL OR (input->'constraints'->>'deadline_ms')::bigint > (extract(epoch from now())*1000)::bigint) GROUP BY 1),
d AS (SELECT r.agent_id, r.impression_id, r.item_id, (r.need_id IS NOT NULL) is_need
  FROM replay_logs r WHERE r.served_at >= (extract(epoch from timestamptz '2026-11-20 00:00+08')*1000)::bigint AND r.served_at < (extract(epoch from timestamptz '2026-11-27 00:00+08')*1000)::bigint AND r.request_mode='recommendation' AND r.source_kind='broadcast'),
f AS (SELECT impression_id, agent_id, item_id, max(score) score FROM feedback_logs
  WHERE created_at >= (extract(epoch from timestamptz '2026-11-20 00:00+08')*1000)::bigint GROUP BY 1,2,3)
SELECT CASE WHEN p.n IS NULL THEN 'a_no_need' WHEN p.n<=5 THEN 'b_1-5' ELSE 'c_6+' END grp, d.is_need,
  count(DISTINCT d.agent_id) agents, count(*) deliveries, count(f.score) rated, count(*) FILTER (WHERE f.score>=1) positive,
  round(100.0*count(*) FILTER (WHERE f.score>=1)/NULLIF(count(f.score),0),1) pos_rate_of_rated,
  round(1.0*count(*) FILTER (WHERE f.score>=1)/count(DISTINCT d.agent_id)/7,2) pos_per_agent_day
FROM d LEFT JOIN per p USING (agent_id) LEFT JOIN f USING (impression_id, agent_id, item_id) GROUP BY 1,2 ORDER BY 1,2
;
