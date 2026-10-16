# Automatic updates

CLI 0.6.6 adds signed binary updates, current-host plugin maintenance and a
transport-only account watch. Existing commands, installation verification,
uninstall behavior and host business delivery remain on the main baseline.

## CLI binary

Native and plugin heartbeat plans share the updater. Explicit `skill` and
`plugin` modes enable maintenance; unknown modes leave it disabled. Home and
per-server `auto_cli_update`, `auto_plugin_update` and `auto_skill_sync` settings
retain their current overrides.

The updater checks a signed `cli/latest/release.json` once daily. A verified
Skills minimum may trigger one additional check. The manifest binds each
platform artifact to a size and SHA-256, with a domain-separated Ed25519
signature verified by the compiled trust key. A lock beside the executable
serializes replacement across Homes. The downloaded executable must report
its signed version under the same stable Home before adoption. Probe failures
restore the previous binary. Cancellation waits for rollback before returning.

After installation, the original command is re-executed once with its stable
Home, explicit server and runtime metadata. Authenticated handoffs independently
pin the canonical Home, Agent, principal and endpoint, checking before resuming
and again in the replacement process before business commands. Token rotation
remains allowed. Child errors and termination exit
codes propagate without repeating business work. Native tasks remain finite
heartbeat calls; hosted and standalone watch processes adopt the new CLI through
the lifecycle described in [watch.md](watch.md).

Claude stream replacement keeps reading old stdout until process stdio closes,
then drains local channel notifications before starting the replacement. Failed
notifications remain in the plugin process for retry. CLI streams handle TERM and
INT by closing socket intake and finishing cache/output for an already-received
packet. Forced kills, a crashed plugin, or a server-side read acknowledged before
socket delivery remain outside this local drain guarantee; the cursor does not
replay already-read messages.

Run `eigenflux --homedir <stable-home> update --format json` to check immediately.
This explicit owner command bypasses the daily throttle, keeps the binary lock,
signature, checksum, anti-rollback and probe checks, then executes the new binary
to report its own `cli_version` and PID. It needs no Agent login, does not sync
Skills or update host plugins, and leaves automatic-update settings unchanged.
A failed or blocked request returns a nonzero exit status.

A bootstrap install is required for older CLIs to obtain this updater.
Unconfigured development binaries do not update. Installation permissions and
running-executable replacement restrictions remain explicit failures; no
privilege elevation is attempted.

## Host plugin

OpenClaw maintenance uses one detached CLI worker. It preserves absent/disabled
plugins and pinned versions. The host manager selects the compatible target via
its dry-run resolution. Unsupported output blocks the attempt. npm identity,
recorded installation scope, archive integrity and installed package bytes are
verified before installation; `provenance-invalid` alone is not an integrity
verdict.

All runtime RPC calls target the verified loopback service port. Successful
adoption requires the target plugin version, a PID matching fresh local Gateway
status and ready/running watches using the worker's CLI version for every
configured account. Changed installs require a replacement PID. Busy deliveries
defer maintenance.

A verified install retains its pending target across retries, including when
the previously loaded adapter lacks the runtime RPC. Each attempt may send at
most one managed `plugins.refresh` to recover a missing notification, then wait
within the existing three-minute deadline. The host owns restart deferral;
the worker never forces a service stop. Successful/absent checks use a daily
cache; failed, blocked and interrupted attempts retry after five minutes through
watch eligibility. Cached plans contain no loaded-version claims.

Codex and Claude Code retain their existing host-driven maintenance path. Skills
use the declared executor, verified current scope and live process evidence.
Native scheduler migration changes only a verified owned task's prompt and
requires fresh host readback; it preserves identity, cadence and paused state.

## Update observations

Automatic heartbeat checks and the explicit `update` command share one recorder.
A single `attempt_id` follows CLI check, download, verify, temporary probe,
install, installed-path probe (`adoption`), and replacement-process execution.
`started` marks entry into a phase; it never proves completion. The target version
is attached only after manifest signature verification. Failed verification has
no trusted target. Rollback is reported only after restoration and directory sync.

`installed` proves binary replacement and the installed-path probe. `executed`
comes from the replacement CLI itself. `runtime_ready` comes from a watch only
after both subscriptions are ready and a matching account/version install receipt
exists. Uploaded install receipts remain locally for seven days, capped at eight
versions, so a later `no_update` check cannot break adoption correlation.
The same version with a different Agent or principal never supplies that receipt.

OpenClaw emits check, verify, install and load observations using its worker's
attempt ID. Manager-owned package resolution and download belong to `install`;
there is no independent download observation without a manager callback.
`installed`, `loaded`, `restart_required`, `blocked` and `failed` remain distinct.
Only live host evidence may establish `loaded`.

An authenticated Home stores the existing account-scoped maintenance events and
uploads one batch outside replacement locks. Upload and credential refresh share
a five-second request budget. Metadata uses bounded error codes; free-text errors,
private paths, tokens and principal identifiers never enter telemetry. Observation
failures do not stop an update. The explicit command includes `observation.status`:

- `local_only`: no Agent V2 credentials; output proves only local execution.
- `not_attempted`: no event was recorded for this invocation.
- `queued`: events remain local, including offline upload or backoff.
- `reported`: the recorded events were acknowledged and no new queue drops occurred.
- `record_failed`: local observation could not be preserved, including unreadable
  state/credentials, identity changes or queue drops during the attempt.

The queue retains at most 256 events for seven days and retries during subsequent
eligible invocations; an exited command does not leave a dedicated upload daemon.
For online acceptance, keep Home, server alias, endpoint, Agent and principal fixed.
Changing an endpoint behind an existing alias reuses that account queue path;
use a separate Home or alias for another environment instead of repurposing it.

The observability repository provides the **Update maintenance** dashboard under
System health. It shows phase timelines and the latest observation per
Agent/attempt/component, with version and error filters. The dashboard must be
merged and deployed separately before it can be used in production. Missing events
or an `installed` event alone cannot establish a running replacement process.

## Publication and verification

The release build generates the signed manifest after all final platform
artifacts and checksums exist. Publication keeps the six-platform preflight and
public-download checks, validates immutable versioned objects, uses conditional
writes, then publishes the signed latest pointer. CLI and Skills publication
share a serialization group. Skills publication verifies an available signed
CLI and all platform digests first.

Run `go test ./...` in `cli/`, the publication tests under `tests/cli_release`,
and cross-platform builds. Local upgrade tests use signed fixtures and real
subprocesses to cover signature rejection, rollback, cancellation, original
arguments, identity preservation, process replacement and host recovery.
