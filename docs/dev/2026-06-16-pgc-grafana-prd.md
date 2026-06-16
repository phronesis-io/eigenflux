# PGC Grafana Dashboard PRD

Date: 2026-06-16
Owner: PGC / EigenFlux operations
Status: implemented in `configs/grafana/dashboards/pgc-pipeline.json`

## Problem

The PGC Grafana dashboard had data, but it was not effective as a professional
operations surface. It mixed lifetime counters, live health, source quality,
cost, and logs without a clear incident-response path. Operators could see
numbers, but had to infer whether the system was healthy, where a bottleneck
was, and which source or stage needed attention.

This became painful during recent PGC incidents where a critical source was
alive upstream but not deliverable downstream. The dashboard must make similar
failures obvious without requiring ad hoc SQL or shell inspection.

## Goals

- Give an operator a 30-second command-center view of PGC health.
- Separate live health, flow, reliability, source health, cost, and logs.
- Prefer rates, rolling windows, and ratios over raw lifetime totals when the
  question is operational.
- Keep drilldown panels close to the summary metric they explain.
- Use the stable Grafana datasource UID `pgc-prometheus` for PGC Prometheus
  panels and `loki` for logs.
- Keep the dashboard provisionable from JSON and testable through Grafana's API.

## Non-Goals

- This PRD does not add new Prometheus metrics.
- This PRD does not replace Lark/webhook canaries; Grafana is the operator
  cockpit, while webhooks remain the push-alert surface.
- This PRD does not create alert rules. Alerting can be layered on top once the
  dashboard panels settle.

## Users

- On-call engineer: needs to know whether PGC is stuck, degraded, or merely
  noisy.
- Product/operator: needs to see whether important sources and topics are being
  delivered.
- Backend engineer: needs to isolate whether a problem is crawl, extract, LLM,
  publish, source inventory, or external API budget.

## Dashboard Structure

1. Command Center
   - Published in the last hour
   - Active queue
   - Maximum worker age
   - Blocked feeds
   - LLM failures in the last hour
   - NewsAPI budget used

2. Flow and Backlog
   - Queue by stage
   - Published throughput per hour
   - Last crawl/process/publish round results
   - 24-hour item mix by status

3. Reliability
   - Worker heartbeat age
   - Worker file descriptor pressure
   - Extraction/LLM/publish error pressure
   - Stage latency
   - Pipeline latency p95

4. Source Health and Coverage
   - Blocked feed count
   - Top failing feeds
   - Per-source publish yield
   - Top published sources
   - Top consumed sources
   - Average heat by source

5. Cost and Quality Gates
   - LLM calls by status
   - LLM latency p95
   - Token usage rate
   - NewsAPI budget and divergence
   - Signal-gate verdicts and drop rate

6. Logs
   - PGC pipeline Loki stream

## Acceptance Criteria

- Grafana dashboard loads with no "data source not found" errors.
- Every Prometheus panel uses `uid=pgc-prometheus`.
- Loki log panel uses `uid=loki`.
- Representative panel queries return non-empty frames through Grafana API.
- Dashboard JSON is valid, provisionable, and committed to git.
- `scripts/local/validate_pgc_grafana_dashboard.py` passes static validation and
  the production Prometheus query sweep.
- Production checkout is clean after deployment except ignored operational
  backups.

## Follow-Ups

- Add Grafana alert rules for max worker age, queue depth, LLM error spikes,
  and blocked-feed spikes.
- Add a source-canary metric so the Paul Graham/latest-source webhook state can
  also be charted in Grafana.
- Add a topic coverage panel once demand-canary metrics are exported.
