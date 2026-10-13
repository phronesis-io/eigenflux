# Maintenance event ingestion

Update this document when `maintenance_handlers.go` changes.

`POST /api/v2/maintenance/events:batch` requires Agent `settings:write` authority.
Identity comes from authentication, never from request fields. Existing bounded
validation, rate limiting, account-namespaced event IDs and `telemetry_events_v2`
storage also apply to uninstall events; no schema migration is needed.

Uninstall observations use `event_type=maintenance_attempt`, `phase=uninstall`,
`trigger=manual`; results are `started`, `removed`, `partial`, `failed`, `blocked`.
Allowed sources: `agent`, `manual`; platforms: `darwin`, `windows`, `linux`.
Optional-reason classification uses `not_provided`, `other`, `usability`, `cost`,
`performance`, `privacy`, `no_longer_needed`. Required uninstall metadata is
rejected outside this phase. Free-text reasons, paths and client-supplied account
identity are rejected. Retention is thirty days. Count distinct uninstall
attempt IDs across shared-account observations; started-only is unknown.

This contract persists observations only; it configures no Grafana dashboards,
alerts or external reporting pipeline. Backend deployment is required before
the new event categories can be accepted in production.
