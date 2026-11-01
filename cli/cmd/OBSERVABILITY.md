# Commission observations

Update this file and [the contract](../../docs/dev/commission-observability.md)
when changing stage or receipt logic.

`watch_commission_telemetry.go` records account-scoped diagnostics through the
existing maintenance queue. Commission workers call it at admission, preflight,
Agent execution, input/artifact verification, upload, delivery and retry. Main
notice receipts retain hashed host session/run IDs. Material reminders retain
the order version and request hash/round. Observation failures never repeat an
operation or change a job/Order state; business records remain authoritative.

# CLI update observations

Heartbeat and watch share the existing maintenance queue and attempt IDs.
Failures use bounded `error_code` categories from the updater: HTTP status,
network cause, installation substep and filesystem category. Original errors
remain in local update state/diagnostics; the queue sends no raw error text.

Policy failures use `update_policy_<step>_<cause>` and retain `waiting_online`.
Aggregate the `update_policy_` prefix, including legacy `update_policy_unavailable`.
Distinguish an onboarding requirement, HTTP/authentication rejection, timeout,
network failure, malformed response and cache read/write error. Both automatic
entries retain the effective account/endpoint cache or the enabled default;
these observations do not change the server switch or update eligibility.
Policy-enabled reports are effective state, not proof of a successful policy GET.
Only clients running the new diagnostics emit these codes. Historical generic
codes and missing evidence retain their original meaning.

# Feed and profile refresh failures

`profile_refresh_events.go` preserves coarse `failure_code` counters and sends
an optional allowlisted `failure_reason` for failed Feed pulls and profile
refresh attempts. Feed V2 validation wraps errors with fixed diagnostic codes
without changing their user-facing text or `errors.Is` semantics. Baseline Feed
containing formal context is `client_error` / `baseline_formal_context`.
Network, parsing, local-storage, API status and context-snapshot errors have
separate bounded reasons. Unknown errors remain `client_other`; raw errors,
response bodies, paths, URLs and credentials never enter the observation.

Deploy the API receiver first, then release the CLI. Older clients omit reasons
and remain accepted. Older receivers reject new properties, and historical
coarse errors cannot be reconstructed. Observation failure never changes Feed
output, retries a business request or changes its outcome. See the
[refresh funnel contract](../../docs/dev/api_endpoints.md#refresh-funnel-observations).
