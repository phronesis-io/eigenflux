# Installation lifecycle and retention evidence

This contract adds diagnostic observations. It does not grant permissions,
complete onboarding, change installation receipts, or create retention activity.
Registration, receipt acknowledgement, drafts, completed onboarding and successful
Feed/action tables remain the authoritative business facts.

## Client rollout and coverage

CLI 1.0.54 and synchronized onboarding Skills create a bounded local observation
queue under the owner-selected Home, separated by the normalized API endpoint.
The POSIX and Windows installers create the session before downloading the CLI
when no existing identity credentials are present. Download failures can therefore
be queued even when the binary is unavailable. Missing cryptographic/upload tools,
unsupported hosts and offline clients remain incomplete coverage, never success.
Set `EIGENFLUX_INSTALL_OBSERVATIONS=0` to disable state creation and upload.

Local files are private; derived symlinks are rejected. A session binds once to
the first authenticated Agent before events enter its queue. Account switches
cannot append to or upload the prior Agent's observations. Proof headers never
follow redirects. Events retain their IDs and times on retry; a queued observation
is confirmed only when its own batch receives a valid server acknowledgement.
The queue holds at most 128 events, sends at most 20 at a time and retains events
for seven days. Explicit new attempts trigger bounded cleanup of expired owned
queues; at most 16 retained attempt queues are allowed.

Automatic CLI observations cover local identity initialization, credential save,
installation receipt transport, and fresh pre-onboarding baseline responses after
validation and rendering. Empty baseline responses count as success; cached-page
reuse and baseline fallback after completed onboarding do not count as another
pre-onboarding request. Permission answers, host integration verification,
profile preparation, actual link/result delivery and the optional first check
remain explicit observations in the onboarding Skill. They are client statements,
not independent proof of human reading or consent. The first-check attempt ID
must be retained across request, execution and actual result delivery.

Scheduler setup can report an explicitly verified unique bound task, separately
from existing scheduler readback and runtime-cycle evidence. There is no complete
cross-host history of scheduled due times: absence of a trigger cannot establish
a missed or late scheduled run. Plugin-native readback remains not applicable
where the host exposes no native scheduler inventory.

During the same installation session, explicit `context pull` and runtime access
checks attach the private correlation headers only to `/agent-context`, with
redirects disabled. The server keeps the first outcome of each kind per session;
ordinary accounts without an installation session do not generate this event.
The observation does not grant access or count as retention activity.

## Client endpoints

- `POST /api/v2/install-telemetry/events:batch`: anonymous pre-registration events.
- `POST /api/v2/agents/me/install-telemetry/events:batch`: Agent V2 Bearer with
  either `onboarding:write` or `settings:write`. Restricted onboarding identities
  can report without waiting for website completion.

Create and persist one random 16-byte lowercase-hex `install_session_id` and one
random 32-byte lowercase-hex proof before the first installation attempt. Keep
both in the same local installation state across process restarts. Send the proof
only in `X-Install-Session-Proof`; never put it in events, logs, URLs or messages.
The server stores only its SHA-256 hash. A session is created by the first valid
batch. A later authenticated batch binds it once to the authenticated Agent.
Another Agent cannot rebind it. Anonymous requests cannot specify an Agent.

```json
{
  "install_session_id": "0123456789abcdef0123456789abcdef",
  "events": [{
    "event_id": "fedcba9876543210fedcba9876543210",
    "event_at": 1795680000000,
    "event_type": "component_verify",
    "result": "succeeded",
    "component": "cli",
    "runtime_name": "codex",
    "runtime_mode": "skill"
  }]
}
```

Generate each `event_id` once (32–64 lowercase hex characters) and reuse the
identical event on retry. Do not regenerate timestamps on retry. An optional
`attempt_id`, also 32–64 lowercase hex, links events from one component attempt
or one explicitly requested first check. Use the same first-check attempt ID for
request, execution and result delivery; do not join unrelated checks by Agent.

| Event type | Anonymous allowed | Meaning |
| --- | --- | --- |
| `install_started` | yes | The installer actually started |
| `host_detected` | yes | Runtime identification was attempted |
| `component_install`, `component_verify` | yes | Install or verify the named component |
| `permission_scheduling`, `permission_execution` | yes | Two independent permission decisions |
| `host_restart` | yes | Required restart and subsequent verification |
| `profile_mode` | yes | Explicit `choice=prefill` or `choice=manual` |
| `profile_read_permission` | yes | Permission to inspect local profile material |
| `profile_draft` | yes | Local privacy-filtered draft preparation |
| `identity_local`, `scheduler_setup` | yes | Stable local identity and persistent task setup |
| `credentials_saved` | no | Client reports durable credential save |
| `install_receipt_sent`, `install_receipt_response` | no | Actual acknowledgement request and verified server response |
| `console_link_delivered` | no | Client reports presenting the Console link |
| `baseline_feed` | no | Read-only baseline request outcome; zero count is valid |
| `attention_prefill` | no | Prefill outcome; no eligible candidate is a valid skip |
| `onboarding_unlocked` | no | Client observed the unlocked context |
| `first_check_requested` | no | User explicitly requested the optional first check |
| `first_check`, `first_check_result` | no | Execution and presentation outcome |

Allowed fields are strict; unknown fields and trailing JSON are rejected.

- `result`: `started`, `succeeded`, `failed`, `declined`, `skipped`, `pending`.
- `component` (optional): `cli`, `skills`, `plugin`, `scheduler`, `identity`, `console`.
- `runtime_mode` (optional): `skill`, `plugin`, `cli`, `unknown`.
- `runtime_name` (optional): `unknown`, `codex`, `openclaw`, `workbuddy`, `hermes`,
  `deepseek`, `deepseek-harness`, `dsh`, `claude-code`, `claude`, `cursor`, `opencode`, `gemini-cli`,
  `gemini`, `qoder`, `trae`, `kilo`, `roo`, `cline`, `windsurf`, `antigravity`,
  `kimi`, `kiro`, `jarvis`. Report unrecognized products as `unknown`; do not infer
  a product from its integration mode. Normalize `dsh` and `deepseek-harness` to DeepSeek in reporting.
- `reason` (optional): `network_error`, `timeout`, `permission_denied`,
  `user_declined`, `user_cancelled`, `not_applicable`, `no_candidates`, `no_content`,
  `validation_failed`, `host_unavailable`, `authentication_failed`, `server_error`,
  `unknown_failure`, `restart_required`, `already_configured`, `manual_selected`.
- `choice`: `prefill` or `manual`, only for `profile_mode`.
- `count`: integer 0–10000, only for `baseline_feed`, `attention_prefill`, `first_check`.

Never send chats, profile text, email, file paths, host error text, tokens, URLs,
environment values or arbitrary properties. Client assertions are always stored
as `client_reported`, including authenticated assertions. They are not proof of
user consent, runtime execution or result presentation beyond client coverage.

Maximum body: 32 KiB; 1–20 events per batch; maximum 512 accepted events per
installation session (client and correlated server observations together).
Server observations without an installation session have a shared limit of 512
events per authenticated Agent per UTC day. Exceeding it drops only the diagnostic
observation, never changes the business response, and emits a fixed `quota` drop
reason. Missing observations therefore cannot be interpreted as business failure.
The existing bounded IP limiter allows 15 requests/minute per gateway process.
Shared PostgreSQL quotas additionally allow at most 256 new sessions per IP
digest/day and 10000 new sessions globally/day, across gateway replicas. Quota
days use UTC epoch-day buckets and do not define analytics cohort dates.
Client event time must be within the previous seven days and no more than one
minute in the future. Both `event_at` and server `received_at` are retained.
Clock errors are rejected; offline loss remains missing evidence, never failure.

HTTP 202 response:

```json
{"data":{"accepted_events":1,"install_session_id":"0123456789abcdef0123456789abcdef","bound":false}}
```

An exact retry returns zero newly accepted events. HTTP 400 means invalid input;
403 means wrong proof; 409 means different payload under an existing event ID or
an attempted identity rebind; 410 means expired session; 429 means quota/rate
limit; 503 means temporary storage failure. Errors contain fixed messages only.
Reporting failure must not fail installation or fabricate a success observation.

## Server evidence and identity correlation

Include `X-Install-Session-ID` and `X-Install-Session-Proof` on provisioning and
related installation operations. Provision success derives the Agent ID from
the verified handler response, never the client body. A valid proof permits
linking only an already-existing session. No proof means no guessed link.

`install_lifecycle_sessions` holds `install_session_id`, `proof_hash`, nullable
`agent_id`/`bound_at`, `created_at`, `expires_at`, and `event_count`.
`install_lifecycle_events` holds `event_key`, `event_id`, nullable
`install_session_id`, nullable `agent_id`, `event_type`, `result`, `evidence`,
`properties`, `payload_hash`, `event_at`, `received_at`, and `expires_at`.
Dashboard readers use `grafana_install_lifecycle_sessions` and
`grafana_install_lifecycle_events`, which omit `proof_hash` and `payload_hash`.
Grant dashboard roles only these projections, never the session verifier table.
`grafana_install_browser_events` projects existing `dashboard_first_render`
client events into the same hashed website journey. It omits original session
IDs and arbitrary properties. A render is client-reported, not proven by link
exchange or a context read.
Anonymous event rows retain NULL `agent_id` after binding. Their session linkage
can be joined through the session table, but does not turn them into authenticated
or server-observed events. Both foreign keys cascade on physical Agent deletion.

The server-only types are `server_registration`, `server_install_receipt`,
`server_profile_draft`, `server_onboarding_step`, `server_handoff`, `server_handoff_exchange`,
`server_context`, `server_email_verify`, and `server_attention_prefill`.
They record the real handler's returned outcome (`succeeded`, `failed`, or receipt
`skipped` with `reason=not_recorded` when `recorded=false`; a missing receipt row
cannot be distinguished from unsupported installation by that response alone).
Properties contain only `http_status`, a fixed
failure `reason`, optional `created`/`recorded`, revision, known state, step 2–5,
and `journey_id=SHA256(console_session_id)` for an authenticated website session.
Successful handoff exchange records its committed new Console session as a
journey and establishes link consumption, not page rendering. Failed anonymous
exchanges without an authenticated actor or installation proof are not attributed.
They never retain request/response content. `server_context` requires an explicit
installation session and keeps only the first success/failure per session, so
normal heartbeat context polling does not grow this installation stream.

These are request outcomes, including legitimate retries. A failed HTTP response
does not imply its underlying transaction never committed. Registration counts
must use actual Agent creation; website completion must use `completed_at`;
receipt rates must use `agent_install_receipts` and its support flag. Server
observation persistence is best effort with a bounded one-second deadline and
does not alter business responses. Missing observations are unknown, not failures.

No session or attempt identifier may be invented to force a linear funnel.
Baseline can precede website completion; manual profile entry, empty baseline,
zero eligible Attention candidates and skipping the optional foreground first
check are valid branches. Existing maintenance/runtime events remain the source
for scheduler readback and actual recurring executions.

Observations and sessions expire after 180 days; bounded existing retention jobs
delete expired event batches, then unreferenced expired sessions. Quota buckets
expire after seven days. These new rows do not backfill historical client evidence.
Deployment requires migration 000145 before API/cleanup rollout; instrumentation
coverage starts only after the corresponding client and server release.

## Pre-CLI installer coverage

The POSIX and Windows installers initialize the same private queue before
fetching the CLI release. Initial installation start, host identification, CLI
installation and Skills file synchronization are client observations. A CLI
download failure can therefore be observed before registration or even before a
working CLI exists. Writing Skills files does not assert that the host read them;
plugin loading, permissions and actual execution require their own observations.

The installer checks only credential-path existence inside the selected Agent
Home. Any V2/legacy credentials, a bound session, an expired/corrupt session or
unsafe linked observation state suppresses anonymous initial-install reporting.
Retries reuse the existing unbound session. Updates never create a new initial
installation observation merely because the installer ran again. Unsupported
custom URL syntax, unavailable cryptography, permissions and offline transport
produce missing coverage without changing installer exit codes.

POSIX uses OpenSSL (no Python/jq), private files and atomic create-if-absent hard
links; Windows uses native cryptography, a current-user-only inherited ACL and
atomic non-overwriting moves. Each event has its original random ID and time.
Proof is sent only in an HTTP header, with redirects disabled; POSIX passes it
through curl configuration stdin, with `.curlrc` disabled. The installer retains
events even after HTTP 202. Only the CLI verifies the acknowledgement and drains
exact retries. `EIGENFLUX_INSTALL_OBSERVATIONS=0` disables this path completely.

`go test ./tests/installhome` exercises real POSIX scripts against fake downloads
and a local curl stub, including a failure before a CLI exists, unchanged failure
exit status, offline reporting, retry identity, opt-out, existing identities,
bound sessions and linked state. In `cli/`,
`go test ./cmd -run TestWindowsInstallerObservationsBeforeDownload` executes the
PowerShell installer with a failing download and an in-process HTTP stub on the
existing Windows platform CI runner; non-Windows hosts skip that test.
