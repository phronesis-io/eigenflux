WITH cohort AS MATERIALIZED (
 SELECT a.agent_id FROM agents a CROSS JOIN LATERAL (
 SELECT count(DISTINCT (to_timestamp(f.feedback_at/1000.0) AT TIME ZONE 'Asia/Shanghai')::date) days
 FROM feedback_logs f WHERE f.agent_id=a.agent_id AND f.score IN(-1,0,1,2)
 AND f.feedback_at>=1791129600000 AND f.feedback_at<1791734400000 OFFSET 0
 ) s WHERE NOT coalesce(a.is_official,false)
 AND lower(coalesce(a.email,'')) NOT LIKE '%@pgc.eigenflux.one'
 AND lower(coalesce(a.email,'')) NOT LIKE '%@bot.eigenflux.one' AND s.days>=5
), days AS (SELECT unnest(ARRAY[date '2026-10-10',date '2026-10-11',date '2026-10-13']) AS day),
feedback AS MATERIALIZED (
 SELECT c.agent_id,(to_timestamp(f.feedback_at/1000.0) AT TIME ZONE 'Asia/Shanghai')::date AS day,f.score,
 CASE WHEN lower(a.email) LIKE '%@pgc.eigenflux.one' THEN 'pgc'
 WHEN a.agent_id IS NOT NULL AND NOT coalesce(a.is_official,false) AND lower(coalesce(a.email,'')) NOT LIKE '%@bot.eigenflux.one' THEN 'ugc' ELSE 'other' END lane
 FROM cohort c CROSS JOIN LATERAL (SELECT item_id,score,feedback_at FROM feedback_logs f WHERE f.agent_id=c.agent_id
 AND f.feedback_at>=(extract(epoch FROM timestamptz '2026-10-10 00:00+08')*1000)::bigint
 AND f.feedback_at<(extract(epoch FROM timestamptz '2026-10-14 00:00+08')*1000)::bigint AND f.score IN(-1,0,1,2) OFFSET 0) f
 LEFT JOIN raw_items i USING(item_id) LEFT JOIN agents a ON a.agent_id=i.author_agent_id
), scores AS (
 SELECT day,agent_id,count(*) feedback,count(*) FILTER(WHERE score>=1) positive,
 count(*) FILTER(WHERE score>=1 AND lane='pgc') pgc_positive,count(*) FILTER(WHERE score>=1 AND lane='ugc') ugc_positive
 FROM feedback GROUP BY 1,2
), deliveries AS MATERIALIZED (
 SELECT (to_timestamp(r.served_at/1000.0) AT TIME ZONE 'Asia/Shanghai')::date AS day,r.agent_id,count(*) delivered
 FROM replay_logs r JOIN cohort c USING(agent_id)
 WHERE r.served_at>=(extract(epoch FROM timestamptz '2026-10-10 00:00+08')*1000)::bigint
 AND r.served_at<(extract(epoch FROM timestamptz '2026-10-14 00:00+08')*1000)::bigint
 AND r.delivered IS TRUE AND r.source_kind='broadcast' AND r.request_mode IS DISTINCT FROM 'search'
 AND (r.item_features #>> '{search,context,input_origin}') IS DISTINCT FROM 'query' GROUP BY 1,2
), grid AS (
 SELECT d.day,c.agent_id,coalesce(s.feedback,0) feedback,coalesce(s.positive,0) positive,
 coalesce(s.pgc_positive,0) pgc_positive,coalesce(s.ugc_positive,0) ugc_positive,coalesce(r.delivered,0) delivered
 FROM days d CROSS JOIN cohort c LEFT JOIN scores s USING(day,agent_id) LEFT JOIN deliveries r USING(day,agent_id)
)
SELECT day::text,count(*) cohort,count(*) FILTER(WHERE feedback>0) scorers,count(*) FILTER(WHERE delivered>0) recipients,
 sum(delivered) delivered,round(avg(delivered),2) deliveries_per_agent,sum(feedback) feedback,round(avg(feedback),2) feedback_per_agent,
 sum(positive) positive,round(avg(positive),2) positive_per_agent,
 percentile_cont(0.5) WITHIN GROUP(ORDER BY positive) median_positive,
 sum(pgc_positive) pgc_positive,sum(ugc_positive) ugc_positive,
 round(100.0*sum(positive)/nullif(sum(feedback),0),2) positive_rate FROM grid GROUP BY day ORDER BY day;
