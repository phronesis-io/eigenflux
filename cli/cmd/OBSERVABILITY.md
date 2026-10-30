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
