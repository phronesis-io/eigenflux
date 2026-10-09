# Agent maintenance observations

Read before changing `maintenance_handlers.go`; update this file with the implementation.

`POST /api/v2/maintenance/events:batch` uses Agent V2 `settings:write` authentication. Actor identity always comes from the authenticated session. Browser telemetry remains a separate endpoint.

Accept 1–50 validated events, at most 128 KiB, from seven days ago through five minutes in the future. Reject unknown fields and invalid categories, version claims or durations. Reuse the existing per-Agent telemetry rate limiter.

Persist a batch atomically in `telemetry_events_v2` as `maintenance_attempt`, with 30-day retention. Prefix the hash of the authenticated Agent ID and client event ID with `maintenance:` before applying the existing unique key. Browser telemetry rejects the colon, so its caller-selected IDs cannot reserve maintenance keys. Retries deduplicate within one Agent; another Agent cannot reserve that key. Return 202 with the number of newly inserted rows; database failure returns 500.

These are client-reported maintenance observations, not proof of order fulfillment or delivery. The local watch keeps business execution independent of ordinary maintenance-upload failures.

No schema migration is required. Validate the HTTP contract and run `TestMaintenancePostgresAgentAuthIdentityAndIdempotency` against an isolated loopback PostgreSQL database; it rolls back temporary test tables. Production verification must not create test telemetry or orders.
