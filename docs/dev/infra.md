# Infrastructure: Tracing, Logging & RPC

## Distributed Tracing

Full-stack OpenTelemetry tracing across all services. Every API request gets a traceId at the gateway, propagated through all downstream RPC services.

### Components

- **pkg/telemetry**: OTel SDK initialization (TracerProvider, OTLP gRPC exporter)
- **pkg/logger**: slog-based structured JSON logging with `logger.Ctx(ctx)` for auto-injected traceId/spanId and `LOG_LEVEL`-controlled verbosity
- **pkg/rpcx**: Kitex OTel client/server suites (automatic span creation for all RPC calls)
- **Hertz OTel middleware**: Root span creation per HTTP request (api gateway + console)

### Monitoring Infrastructure

Monitoring services are defined in `docker-compose.monitor.yml` (separate from core `docker-compose.yml`):

- **Jaeger** (`:16686`): Trace storage and timeline visualization
- **Loki** (`:3122`): Log aggregation with traceId correlation
- **Prometheus** (`:9090`): Metrics scraping + alert rule evaluation
- **Alertmanager** (`127.0.0.1:9093`): External alert delivery (see "External Alerting" below)
- **Grafana** (`:3123`): Unified query UI (Jaeger traces + Loki logs)
- **node_exporter** (compose network only, cloud `public` profile): Host disk/memory/cpu metrics for the monitor host

Start monitoring: `docker compose -f docker-compose.monitor.yml up -d`, then set `MONITOR_ENABLED=true` and `LOKI_URL=http://localhost:3122` in `.env`. Without these env vars, services run with local structured stdout logging only -- no tracing overhead.

### Usage

- **View traces:** Jaeger UI at `http://localhost:16686`, select a service, search traces
- **Search logs by traceId:** Grafana at `http://localhost:3123` -> Explore -> Loki -> query `{service=~".+"} | json | traceId="<id>"`
- **Trace-to-log correlation:** In Grafana, Jaeger traces link to Loki logs and vice versa

## Logging Convention

All service code uses the project logger wrapper. Do not call `slog` directly outside the `pkg/logger` or `console/.../internal/logger` packages.

- **`logger.Ctx(ctx)`** -- returns a logger enriched with traceId/spanId from the OTel span in `ctx`. Use this in request/RPC handlers, middleware, and any code running inside a traced lifecycle.
- **`logger.Default()`** -- returns the process-wide structured logger without request-scoped trace fields. Use this in startup/init code, background workers, and fire-and-forget goroutines.
- **`LOG_LEVEL`** -- controls the minimum emitted level process-wide. Supported values: `debug`, `info`, `warn`, `error`. Local default is `debug`.

```go
// In request handlers and middleware (has ctx with active span):
logger.Ctx(ctx).Info("FetchFeed called", "agentID", req.AgentId)
logger.Ctx(ctx).Error("operation failed", "err", err)

// In startup/init code:
logger.Default().Info("service initialized", "port", port)

// In background goroutines:
go func() {
    logger.Default().Error("async ack failed", "err", err)
}()
```

## RPC Bootstrap Conventions (pkg/rpcx)

`pkg/rpcx/options.go` provides canonical helpers for constructing Kitex client and server options. All RPC clients and servers must use these helpers instead of configuring Kitex options manually.

- `ClientOptions(resolver, ...extra client.Option) []client.Option` -- returns a base option set with TTHeader transport, transmeta codec, and a 10s RPC timeout. Pass additional options via `extra` to override defaults.
- `ServerOptions(addr string, registry registry.Registry, serviceName string, ...extra server.Option) []server.Option` -- returns a base option set with the given address, etcd registry, TTHeader transport, and transmeta codec. Pass additional options via `extra` to override defaults.
- Default RPC timeout is **10s**. Override per-call with `callopt.WithRPCTimeout` or globally via an extra option.
- TTHeader + transmeta are always enabled, ensuring `metainfo.PersistentValue` keys (including `ef.*` reqinfo keys) are propagated across all hops without per-service configuration.

## Prometheus Metrics

All services expose Prometheus metrics on a dedicated port (service port + 1000). The pipeline uses port 9070, console uses 9091.

### Metric Names

| Metric | Type | Labels | Used By |
|--------|------|--------|---------|
| `http_request_duration_seconds` | Histogram | method, path, status | API gateway, console |
| `http_requests_total` | Counter | method, path, status | API gateway, console |
| `http_requests_in_flight` | Gauge | — | API gateway, console |
| `rpc_request_duration_seconds` | Histogram | service, method, status | All RPC services |
| `rpc_requests_total` | Counter | service, method, status | All RPC services |
| `consumer_messages_processed_total` | Counter | stream, status | Pipeline consumers |
| `consumer_message_duration_seconds` | Histogram | stream | Pipeline consumers |
| `consumer_lag` | Gauge | stream, consumer_group | Pipeline lag poller |
| `consumer_retry_total` | Counter | stream | Pipeline consumers |
| `item_publish_to_process_duration_seconds` | Histogram | — | Item consumer |
| `llm_call_duration_seconds` | Histogram | prompt | Pipeline LLM client |
| `llm_reasoning_tokens` | Histogram | prompt | Pipeline LLM client |
| `llm_completion_tokens` | Histogram | prompt | Pipeline LLM client |

### Metrics Ports

| Service | Metrics Port |
|---------|-------------|
| API Gateway | 9080 |
| Console API | 9091 |
| Profile RPC | 9881 |
| Item RPC | 9882 |
| Sort RPC | 9883 |
| Feed RPC | 9884 |
| PM RPC | 9885 |
| Auth RPC | 9886 |
| Notification RPC | 9887 |
| Pipeline | 9070 |
| WebSocket | 9088 |

### Grafana Dashboards

Three provisioned dashboards available at `http://localhost:3123`:

- **API Gateway** (`eigenflux-api`) — request rate, p50/p99 latency, error rate, status codes
- **RPC Services** (`eigenflux-rpc`) — service health, per-service latency/errors, top methods
- **Pipeline Consumers** (`eigenflux-pipeline`) — consumer lag, processing rate, failures, retries, publish-to-process latency, LLM call duration/token usage

### Starting the Monitoring Stack

**Local dev** (services on host, monitoring in Docker):

```bash
docker compose -f docker-compose.monitor.yml up -d
```

Prometheus scrapes `host.docker.internal:*` by default. Grafana at `http://localhost:3123`.

**Cloud** (app server and monitor server are separate ECS instances):

```bash
METRICS_HOST=<app-server-internal-ip> \
docker compose -f docker-compose.monitor.yml up -d
```

`METRICS_HOST` is the internal IP of the app server where Go services run. The `prometheus-init` container substitutes this into the Prometheus scrape config at startup.

Ensure the app server's firewall allows inbound on metrics ports (9070, 9080, 9088, 9091, 9881-9887, 9100 for node_exporter) from the monitor server.

**Dashboard provisioning**: All 3 dashboards are JSON files in `configs/grafana/dashboards/`. They are volume-mounted into Grafana and loaded automatically on startup. No manual import needed — any changes to the JSON files take effect on Grafana restart.

Set `MONITOR_ENABLED=true` in the app server's `.env` to enable distributed tracing alongside metrics.

### External Alerting

The monitor server is the independent watcher for the app server: it must be able to page someone even when the app host (and every alerter running on it, e.g. the PGC pipeline's in-process Lark alerter) is dead. The pieces:

- **Alert rules** — `configs/prometheus/alert_rules.yml`, copied into the prometheus-config volume by `prometheus-init`. Covers: PGC metrics endpoint down / scrape target absent, a dead-man's switch on `pgc_metrics_last_refresh_success_timestamp` (metrics surface frozen while `up` stays 1), host down, disk <10% free, disk predicted full within 72h, and an always-firing `Watchdog` heartbeat.
- **Alertmanager** — `alertmanager` compose service (config template `configs/alertmanager/alertmanager.yml`). Alerts are grouped per `alertname`+`job`, criticals re-notify every 4h, warnings every 24h. Delivery goes to the webhook in `ALERTMANAGER_LARK_WEBHOOK`.
- **`ALERTMANAGER_LARK_WEBHOOK`** (`.env` on the monitor host, **required in cloud**) — must be a different channel than the Lark webhook the app host uses, so the alert path shares no fate with the thing it watches. `alertmanager-init` substitutes it into the config at startup (Alertmanager has no env expansion); the secret never lives in git. Alertmanager POSTs its standard JSON payload — a plain Lark custom bot rejects that format, so point the URL at a Lark Anycross flow or a small formatting bridge.
- **Host metrics** — the `node-exporter` compose service covers the monitor host (job `node-monitor-host`; cloud-only `public` profile, since binding `/` is not shared on Docker Desktop — the target is expectedly down in local dev). The app server needs its own node_exporter listening on 9100 (job `node-app-host`), e.g.:

  ```bash
  docker run -d --name node_exporter --restart unless-stopped \
    --net host --pid host -v /:/host:ro,rslave \
    prom/node-exporter:v1.9.1 --path.rootfs=/host
  ```

  Until it runs (and 9100 is open to the monitor server), the `node-app-host` target is down and `HostDown` fires for it — install it as part of the same rollout.
- **Grafana-managed alerts** — `configs/grafana/alerting/` provisions a contact point that forwards to the stack's Alertmanager and makes it the root notification policy. Alerts created in the Grafana UI therefore deliver through the same channel instead of the factory placeholder email (`<example@email.com>`, no SMTP) that silently dropped everything.
- **Watchdog** — routed to Alertmanager's null receiver by design. To also catch "the monitor host itself died", point an external dead-man's-switch service (one that pages when it *stops* receiving) at this alert.
- **Remaining known gap** — if the monitor host dies and no dead-man's-switch service is wired to the Watchdog, nothing pages. Everything else (app host death, pipeline death, webhook rot on the app side) is now covered externally.

**Grafana anonymous access**: compose defaults are anonymous=enabled with read-only Viewer role — a redeploy with a missing `.env` can no longer come up anonymously administrable. On the internet-facing cloud instance set `GF_AUTH_ANONYMOUS_ENABLED=false` (dashboards query the prod business DB); use Grafana's per-dashboard public-dashboards feature if something should stay public.

**End-to-end test** after deploying: `docker compose -f docker-compose.monitor.yml stop node-exporter` (or stop node_exporter on the app host), wait ~6 minutes, confirm the `HostDown` notification arrives on the alert channel, then start it again and confirm the resolved notification.
