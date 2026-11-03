# Commission observations

Update this file and [the contract](../../docs/dev/commission-observability.md)
when changing stage or receipt logic.

`watch_commission_telemetry.go` records account-scoped diagnostics through the
existing maintenance queue. Commission workers call it at admission, preflight,
Agent execution, input/artifact verification, upload, delivery and retry. Main
notice receipts retain hashed host session/run IDs. Material reminders retain
the order version and request hash/round. Observation failures never repeat an
operation or change a job/Order state; business records remain authoritative.

Preserve fixed local job codes in `commission.local_code` alongside the coarse
category. Attach structured check receipts and captured, allowlisted bridge
diagnostics. A receipt reports actual presence/outcome, checked version,
inspected-file count and retained fingerprint; an unknown bridge failure stays
unknown. Do not upload raw errors, prompts, process output or local paths.

Startup reports retained blocked, failed or unconfirmed jobs as deduplicated
`diagnostic_snapshot` observations. Snapshotting never invokes the Agent or
replays a business operation. Deploy the API receiver before the matching CLI;
upgrading cannot recover bridge details an older process never saved.

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
Repeated identical policy errors are suppressed across maintenance entries using
Home/server-local account, principal, endpoint and CLI-version scoped state.
A changed reason emits another warning; the first successful fetch emits one
`no_update` / `update_policy_recovered` receipt. Requests, the three-second
settings timeout and cached/default policy behavior remain unchanged. These
warnings do not establish an attempted or failed binary upgrade.

An installed CLI retains the original attempt and identity locally until a
matching new process records `execute.executed`. Watch startup records this
before runtime readiness, including notification-only and supervisor restarts.
`installed`, `executed` and `runtime_ready` mean installation, process execution
and runtime readiness respectively; none implies the next stage. Pending
execution evidence expires with the maintenance retention window. Historical
missing receipts are not reconstructed.
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

# Outer-loop setup and native scheduler readback

`watch start` and the shared upgrade/post-authentication recovery entry record
a `watch` attempt before platform/host checks. Recovery retains its existing
return status while reporting unsupported platforms and setup failures. Bounded
phases distinguish preflight, identity, Skills, host discovery/probe, task
preparation, binding and task startup. Failures retain the original local
error and queue only its stage/category. Disabled tasks remain skipped;
installation does not claim runtime readiness or model work. Unauthenticated
attempts print a bounded local-only diagnostic without Agent attribution.
Authenticated failures use the existing identity-pinned queue and offline retry;
observation failures never change the setup result.

Automatic recovery and commands invoked with `--origin heartbeat` record
`trigger: auto`; owner-requested commands record `trigger: manual`. Recovery
passes its recorder through WorkBuddy setup and task restoration, then restores
the caller context. Each new phase starts a fresh outcome; earlier setup success
cannot hide a later phase failure. A confirmed installation remains installed
when subsequent runtime-readiness checks time out, without claiming live health.

`heartbeat scheduler-report --stdin` records host-reported native API readback
for an existing authenticated Agent. Input carries `observed_at` (Unix ms),
`complete` and `tasks` using the migration inventory contract. Readback must be
at most five minutes old and no more than 30 seconds in the future. Incomplete
inventories, duplicate IDs and ambiguous owned tasks fail closed. Inspection
failures use `scheduler_api_unavailable`, `scheduler_readback_denied` or
`scheduler_readback_failed` instead of an inventory. Only the matching
Home/server task is projected. State is `enabled`, `paused`, `disabled`,
`missing` or `unknown`; task/schedule values use account-scoped SHA-256 hashes.
Names, prompts, raw IDs, schedules and Home paths are not uploaded. `verified`
verifies the readback contract, not live health, prompt correctness, scheduler
execution or Agent work. Existing verified migrations attach the same projection.

Both observations carry the CLI process OS/architecture and independent
container/WSL evidence states. Linux detection reads container marker presence,
cgroup categories, kernel WSL markers and presence of WSL environment markers.
Only fixed enums leave the process. Probe denial stays `unknown`; absent
recognized evidence is `not_detected`. Unsupported probes make no claim about
the physical host. Container and WSL evidence can coexist. Raw kernel text,
hostnames, environment values, task content and credentials are excluded.

Deploy the receiver before the CLI. Older receivers reject `outer_loop`; their
queue remains pending. Skills capability-check the receipt command, so older
CLIs retain their established flow with unknown coverage. There is no historical
backfill or business-retention change. Local five-second watch pulses and
notification-only loops remain separate from setup/readback observations.
