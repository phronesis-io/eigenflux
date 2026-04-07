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
- **Grafana** (`:3123`): Unified query UI (Jaeger traces + Loki logs)

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
