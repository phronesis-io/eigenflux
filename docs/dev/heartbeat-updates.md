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
Home, explicit server and runtime metadata. Child errors and termination exit
codes propagate without repeating business work. Native tasks remain finite
heartbeat calls; hosted and standalone watch processes adopt the new CLI through
the lifecycle described in [watch.md](watch.md).

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
