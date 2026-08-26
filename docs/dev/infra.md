# Infrastructure: Tracing, Logging & RPC

## Distributed Tracing

Full-stack OpenTelemetry tracing across all services. Every API request gets a traceId at the gateway, propagated through all downstream RPC services.

### Components

- **pkg/telemetry**: OTel SDK initialization (TracerProvider, OTLP gRPC exporter)
- **pkg/logger**: slog-based structured JSON logging with `logger.Ctx(ctx)` for auto-injected traceId/spanId and `LOG_LEVEL`-controlled verbosity
- **pkg/rpcx**: Kitex OTel client/server suites (automatic span creation for all RPC calls)
- **Hertz OTel middleware**: Root span creation per HTTP request (api gateway + console)

### Monitoring Infrastructure

The monitoring stack (Jaeger `:16686`, Loki `:3122`, Grafana `:3123`, Prometheus `:9090`, dashboards, alert rules) lives in the private `phronesis-io/eigenflux-observability` repository — clone it and follow its README to start the stack locally or deploy it. This repository keeps only the application-side instrumentation (metrics/tracing/logging code).

Once the stack is running, set `MONITOR_ENABLED=true` and `LOKI_URL=http://localhost:3122` in `.env`. Without these env vars, services run with local structured stdout logging only -- no tracing overhead.

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

Dashboards (API Gateway, RPC Services, Pipeline Consumers, and others) are provisioned by the private `phronesis-io/eigenflux-observability` repository and served by its Grafana at `http://localhost:3123` locally. Dashboard and alert-rule changes go through pull requests in that repository — this repository owns only the metric *producers* documented above.

Business dashboards must not receive direct `SELECT` access to user-level
delivery, feedback, profile, or content tables. Database-backed panels query
purpose-built `grafana_*` security-barrier views that expose fixed anonymous
aggregates. The production Grafana role receives `SELECT` on those views only;
adding a dashboard query is not a reason to broaden its table grants. Migration
`000075_pgc_audience_aggregate_views.sql` defines the PGC audience, demand,
feedback, surface, and profile-completeness contract.

When the app server and monitor server are separate hosts, ensure the app server's firewall allows inbound on the metrics ports listed above from the monitor server, and set `MONITOR_ENABLED=true` in the app server's `.env` to enable distributed tracing alongside metrics. Cross-host bindings and deployment are documented in the observability repository.
