# Monitoring & Dashboards Design Spec

## Goal

Add Prometheus metrics collection and Grafana dashboards to EigenFlux, covering API latency, RPC service health, consumer status, and content/user distribution.

## Architecture

```
Services (API, RPC, Pipeline, Console)
    │  expose /metrics
    ▼
Prometheus (scrapes every 15s)
    │
    ▼
Grafana ──── PostgreSQL (distribution queries)
    │
    ▼
4 Dashboards
```

All services expose a `/metrics` HTTP endpoint on a dedicated metrics port (`service_port + 1000`, e.g. API on 8080 exposes metrics on 9080). Prometheus scrapes all endpoints. Grafana queries both Prometheus and PostgreSQL directly.

## 1. Infrastructure Changes

### 1.1 Prometheus Container

Add to `docker-compose.monitor.yml`:

- Image: `prom/prometheus:v3.4.0`
- Port: `9090`
- Config mount: `configs/prometheus/prometheus.yml`
- Network: same as existing monitor services

### 1.2 Prometheus Scrape Config

File: `configs/prometheus/prometheus.yml`

Scrape targets (all on `host.docker.internal`):

| Job | Target | Port |
|-----|--------|------|
| api-gateway | host.docker.internal:9080 | API metrics |
| console-api | host.docker.internal:9091 | Console metrics |
| profile-rpc | host.docker.internal:9881 | Profile RPC |
| item-rpc | host.docker.internal:9882 | Item RPC |
| sort-rpc | host.docker.internal:9883 | Sort RPC |
| feed-rpc | host.docker.internal:9884 | Feed RPC |
| pm-rpc | host.docker.internal:9885 | PM RPC |
| auth-rpc | host.docker.internal:9886 | Auth RPC |
| notification-rpc | host.docker.internal:9887 | Notification RPC |
| pipeline | host.docker.internal:9070 | Pipeline consumers |
| ws | host.docker.internal:9088 | WebSocket service |

### 1.3 Grafana Datasources

Add to `configs/grafana/datasources.yml`:

- **Prometheus** datasource pointing to `http://prometheus:9090`
- **PostgreSQL** datasource pointing to `host.docker.internal:5432` (read-only user recommended; initial setup uses existing credentials)

### 1.4 Grafana Dashboard Provisioning

New file: `configs/grafana/dashboards.yml` — tells Grafana to load JSON dashboards from `configs/grafana/dashboards/`.

Dashboard JSON files:
- `configs/grafana/dashboards/api-gateway.json`
- `configs/grafana/dashboards/rpc-services.json`
- `configs/grafana/dashboards/pipeline-consumers.json`
- `configs/grafana/dashboards/content-distribution.json`

## 2. Code Changes

### 2.1 New Package: `pkg/metrics`

Central metrics registry and metric definitions. All services import this package.

**File: `pkg/metrics/metrics.go`**

Defines a shared `prometheus.Registry` and pre-registered metrics:

**HTTP metrics** (for API gateway and console):
- `http_request_duration_seconds` — Histogram, labels: `method`, `path`, `status`. Buckets: 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s
- `http_requests_total` — Counter, labels: `method`, `path`, `status`
- `http_requests_in_flight` — Gauge, labels: `handler`

**RPC metrics** (for all Kitex services):
- `rpc_request_duration_seconds` — Histogram, labels: `service`, `method`, `status`. Same buckets as HTTP
- `rpc_requests_total` — Counter, labels: `service`, `method`, `status`

**Consumer metrics** (for pipeline):
- `consumer_messages_processed_total` — Counter, labels: `stream`, `status` (success/failure)
- `consumer_message_duration_seconds` — Histogram, labels: `stream`. Buckets: 10ms, 50ms, 100ms, 500ms, 1s, 5s, 10s, 30s, 60s
- `consumer_lag` — Gauge, labels: `stream`, `consumer_group`. Updated by periodic XPENDING poll
- `consumer_retry_total` — Counter, labels: `stream`

**File: `pkg/metrics/server.go`**

`StartMetricsServer(port int)` — starts a minimal HTTP server serving `/metrics` from the shared registry. Called in each service's `main.go`.

**File: `pkg/metrics/middleware.go`**

`HertzMiddleware()` — Hertz middleware that records `http_request_duration_seconds`, `http_requests_total`, and `http_requests_in_flight` for every request. Path normalization: replace numeric path segments with `:id` to avoid cardinality explosion (e.g., `/api/v1/item/12345` becomes `/api/v1/item/:id`).

### 2.2 Kitex RPC Metrics via Middleware Option

**File: `pkg/metrics/kitex.go`**

`KitexServerMiddleware()` — returns a Kitex `server.Option` middleware that records `rpc_request_duration_seconds` and `rpc_requests_total` per method call.

Registered via `rpcx.ServerOptions()` in `pkg/rpcx/options.go` so all RPC services get metrics automatically.

### 2.3 Consumer Metrics Instrumentation

Instrument each consumer in `pipeline/consumer/`:

- **profile_consumer.go**: Increment `consumer_messages_processed_total{stream="profile:update"}` on success/failure. Record processing duration.
- **item_consumer.go**: Same pattern with `stream="item:publish"`.
- **item_stats_consumer.go**: Same with `stream="item:stats"`. Also track retries via `consumer_retry_total`.
- **replay_consumer.go**: Same with `stream="replay:log"`.

### 2.4 Consumer Lag Poller

**File: `pkg/metrics/lag.go`**

`StartLagPoller(redisClient, interval)` — polls `XPENDING` for each stream/group pair every 10 seconds and updates `consumer_lag` gauge.

Stream/group pairs:
- `stream:profile:update` / `cg:profile:update`
- `stream:item:publish` / `cg:item:publish`
- `stream:item:stats` / `cg:item:stats`
- `stream:replay:log` / `cg:replay:log`

Started in `pipeline/main.go`.

### 2.5 Service Entry Point Changes

Each service's `main.go` adds one line after telemetry init:

```go
go metrics.StartMetricsServer(cfg.ApiPort + 1000)
```

API gateway and console also register the Hertz middleware:

```go
h.Use(metrics.HertzMiddleware())
```

### 2.6 Config Addition

Add to `pkg/config/config.go`:
- `MetricsPortOffset` — int, default `1000`. Metrics port = service port + offset.

Exception: Console API (8090 + 1000 = 9090) would collide with Prometheus. Console metrics port overridden to `9091`.

Pipeline has no service port — use a fixed metrics port `9070`, configured via `PIPELINE_METRICS_PORT` env var (default 9070).

## 3. Grafana Dashboards

### 3.1 API Gateway Dashboard

**Panels:**

| Panel | Type | Query |
|-------|------|-------|
| Request Rate | Time series | `rate(http_requests_total{job="api-gateway"}[1m])` |
| P50 Latency | Time series | `histogram_quantile(0.5, rate(http_request_duration_seconds_bucket{job="api-gateway"}[5m]))` |
| P99 Latency | Time series | `histogram_quantile(0.99, rate(http_request_duration_seconds_bucket{job="api-gateway"}[5m]))` |
| Error Rate | Time series | `rate(http_requests_total{job="api-gateway",status=~"5.."}[1m])` |
| Status Code Distribution | Pie chart | `sum by (status) (increase(http_requests_total{job="api-gateway"}[1h]))` |
| Top Endpoints by Latency | Table | P99 per path, sorted descending |
| Requests In Flight | Gauge | `http_requests_in_flight{job="api-gateway"}` |

**Template variables:** `$interval` (auto)

### 3.2 RPC Services Dashboard

**Panels:**

| Panel | Type | Query |
|-------|------|-------|
| Service Health | Stat (green/red) | `up{job=~".*-rpc"}` |
| Request Rate by Service | Time series | `sum by (service) (rate(rpc_requests_total[1m]))` |
| P99 Latency by Service | Time series | `histogram_quantile(0.99, sum by (service, le) (rate(rpc_request_duration_seconds_bucket[5m])))` |
| Error Rate by Service | Time series | `sum by (service) (rate(rpc_requests_total{status="error"}[1m]))` |
| Top Methods by Latency | Table | P99 per service+method, sorted descending |

**Template variables:** `$service` (multi-select from label values)

### 3.3 Pipeline Consumers Dashboard

**Panels:**

| Panel | Type | Query |
|-------|------|-------|
| Consumer Lag | Time series | `consumer_lag` by stream |
| Processing Rate | Time series | `rate(consumer_messages_processed_total{status="success"}[1m])` by stream |
| Failure Rate | Time series | `rate(consumer_messages_processed_total{status="failure"}[1m])` by stream |
| Success/Failure Ratio | Pie chart | `sum by (stream, status) (increase(consumer_messages_processed_total[1h]))` |
| Processing Duration P99 | Time series | `histogram_quantile(0.99, rate(consumer_message_duration_seconds_bucket[5m]))` by stream |
| Retry Rate | Time series | `rate(consumer_retry_total[1m])` by stream |

**Template variables:** `$stream` (multi-select)

### 3.4 Content & User Distribution Dashboard

All panels query PostgreSQL directly.

**Panels:**

| Panel | Type | SQL |
|-------|------|-----|
| Items by Broadcast Type | Pie chart | `SELECT broadcast_type, COUNT(*) FROM processed_items WHERE status=3 GROUP BY broadcast_type` |
| Items by Source Type | Bar chart | `SELECT source_type, COUNT(*) FROM processed_items WHERE status=3 AND source_type != '' GROUP BY source_type ORDER BY count DESC` |
| Items by Language | Pie chart | `SELECT lang, COUNT(*) FROM processed_items WHERE status=3 AND lang != '' GROUP BY lang` |
| Quality Score Distribution | Histogram | `SELECT width_bucket(quality_score, 0, 1, 10) as bucket, COUNT(*) FROM processed_items WHERE status=3 GROUP BY bucket ORDER BY bucket` |
| Users by Country | Bar chart | `SELECT country, COUNT(*) FROM agent_profiles WHERE country != '' GROUP BY country ORDER BY count DESC LIMIT 20` |
| Profile Completion Status | Pie chart | `SELECT status, COUNT(*) FROM agent_profiles GROUP BY status` |
| Items Created Over Time | Time series | `SELECT to_timestamp(created_at/1000) as time, COUNT(*) FROM raw_items WHERE created_at > extract(epoch from now()-interval '30 days')*1000 GROUP BY 1 ORDER BY 1` |
| Feed Consumption by Type | Bar chart | `SELECT p.broadcast_type, SUM(s.consumed_count) as total FROM processed_items p JOIN item_stats s ON p.item_id = s.item_id WHERE p.status=3 GROUP BY p.broadcast_type ORDER BY total DESC` |
| Top Feedback Items | Table | `SELECT p.item_id, p.broadcast_type, s.consumed_count, s.total_score FROM processed_items p JOIN item_stats s ON p.item_id = s.item_id WHERE p.status=3 ORDER BY s.total_score DESC LIMIT 20` |

## 4. Dependency

Add to `go.mod`:

```
github.com/prometheus/client_golang v1.22.0
```

## 5. Files Changed/Created

### New Files

| File | Purpose |
|------|---------|
| `pkg/metrics/metrics.go` | Metric definitions and registry |
| `pkg/metrics/server.go` | Metrics HTTP server |
| `pkg/metrics/middleware.go` | Hertz metrics middleware |
| `pkg/metrics/kitex.go` | Kitex RPC metrics middleware |
| `pkg/metrics/lag.go` | Consumer lag poller |
| `configs/prometheus/prometheus.yml` | Prometheus scrape config |
| `configs/grafana/dashboards.yml` | Dashboard provisioning config |
| `configs/grafana/dashboards/api-gateway.json` | API dashboard |
| `configs/grafana/dashboards/rpc-services.json` | RPC dashboard |
| `configs/grafana/dashboards/pipeline-consumers.json` | Consumer dashboard |
| `configs/grafana/dashboards/content-distribution.json` | Distribution dashboard |

### Modified Files

| File | Change |
|------|--------|
| `docker-compose.monitor.yml` | Add Prometheus container |
| `configs/grafana/datasources.yml` | Add Prometheus + PostgreSQL datasources |
| `pkg/config/config.go` | Add `MetricsPortOffset` |
| `pkg/rpcx/options.go` | Add Kitex metrics middleware to default options |
| `api/main.go` | Register Hertz metrics middleware, start metrics server |
| `console/console_api/main.go` | Register Hertz metrics middleware, start metrics server |
| `rpc/*/main.go` (7 files) | Start metrics server |
| `ws/main.go` | Start metrics server |
| `pipeline/main.go` | Start metrics server, start lag poller |
| `pipeline/consumer/profile_consumer.go` | Add processing metrics |
| `pipeline/consumer/item_consumer.go` | Add processing metrics |
| `pipeline/consumer/item_stats_consumer.go` | Add processing metrics |
| `pipeline/consumer/replay_consumer.go` | Add processing metrics |
| `go.mod` / `go.sum` | Add prometheus/client_golang dependency |
| `docs/dev/infra.md` | Document metrics and dashboards |

## 6. Testing

- Unit tests for `pkg/metrics/` — verify metric registration, middleware label extraction, path normalization
- Integration test: start metrics server, scrape `/metrics`, assert expected metric names present
- Manual verification: start all services with monitoring stack, confirm dashboards show data in Grafana

## 7. Rollout

Metrics collection is always-on (low overhead). Dashboards are provisioned automatically when Grafana starts. No feature flag needed. The existing `MONITOR_ENABLED` flag controls tracing only; metrics are independent.
