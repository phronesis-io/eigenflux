# Automatic updates

CLI 0.6.6 adds signed CLI updates and current-host plugin maintenance to the
existing heartbeat plan. Existing scheduling, business stages and account
commands remain on the main baseline.

## CLI binary

Explicit skill and plugin modes enable maintenance. Unknown modes leave it
disabled. Home and per-server auto_cli_update, auto_plugin_update and
auto_skill_sync settings retain their overrides.

Check the signed cli/latest/release.json once daily. A verified Skills minimum
may admit one additional check. Verify the domain-separated Ed25519 signature,
platform size and SHA-256 using the compiled trust key. Serialize replacement
with a lock beside the shared binary. Probe the temporary and installed binaries
under the original Home. Atomically replace the binary, retain its previous
bytes and synchronously restore them if the installed-path probe fails.
Cancellation completes rollback before returning. Reject signed version rollback.

After installation, execute the original command once with its stable Home,
explicit server and runtime metadata. Authenticated handoffs pin canonical Home,
Agent, principal and endpoint before resuming and before replacement-process
business commands. Token rotation remains allowed. Preserve child exit codes and
forward termination signals, with a bounded forced-stop fallback.

Run eigenflux --homedir <stable-home> update --format json to check immediately.
This owner command bypasses only the daily throttle. Missing or unreadable account
files do not block manual binary updates. It needs no login and reports
the replacement process version, PID and Home. It does not change update switches.
A bootstrap install is required for older CLIs. Installation permission failures
and running-executable replacement restrictions remain explicit failures.

Claude adopts the new CLI through its existing stream lifecycle. Keep reading old
stdout until process stdio closes, then finish received channel notifications
before replacement. Retry failed notifications within the plugin process.
CLI stream handles TERM/INT by stopping socket intake and completing cache/output
for an already-received packet within a three-second drain window. A second signal
or the deadline returns an explicit incomplete-drain error. Forced kills, plugin crashes and server-side
mark-read before socket delivery remain outside this local drain guarantee.

## Host plugin

OpenClaw maintenance runs in one detached CLI worker. Preserve absent/disabled
plugins and pinned sources. Use the published manager's dry-run to discover the
compatible target; block unsupported output. Verify npm identity, recorded install
scope, archive integrity and installed package bytes. A provenance-invalid catalog
classification alone does not prove changed package bytes.

Use the published plugins update command. Its source-change handling owns the
idle-deferred Gateway restart. Do not force-stop the Gateway or require additional
EigenFlux runtime RPCs. After a changed install, report installed followed by
restart_required. A later heartbeat may report loaded only when the actual calling
plugin version matches the verified installed target. A worker inherited from the
old plugin cannot establish that evidence. Cached checks never claim a loaded
version. Successful/absent discovery uses a daily cache; failures and pending
restart retry after five minutes on a subsequent heartbeat.

Claude Code and Codex use their existing official managers through the declared
host-driven maintenance instructions. Preserve installation scope and permissions;
verify the installed version and distinguish it from current-process load evidence.

## Observations

Automatic and manual CLI updates share check, download, verify, probe, install,
installed-path adoption and execute events. One attempt_id crosses the CLI handoff;
the child reports its own executed version. Target versions come only from verified
manifests. Started means phase entry. Rolled_back requires successful restoration
and directory sync. Plugin events distinguish check, verify, install and load;
manager-owned downloads belong to install.

Use the existing account-scoped maintenance queue: at most 256 events for seven
days, one oldest-first batch of fifty per flush, bounded backoff and stable event
IDs. Store fixed error codes rather than tokens, paths or free-text errors.
Uploads have a five-second HTTP timeout and use the current token. Normal CLI
business owns credential refresh; expired/rejected tokens leave events queued
until a subsequent invocation. Observation failures do not stop replacement.

The explicit command observation status describes events recorded by that process:
local_only means no Agent V2 credentials; not_attempted means no events; queued
means no acknowledgement observed; reported means its events were acknowledged;
record_failed means a local write failed. A child does not attest completeness of
its parent's observations. Inspect the shared queue and server timeline separately.
Keep Home, server alias, endpoint, Agent and principal fixed for online acceptance.
Use a separate Home or alias for another environment.

The separate observability dashboard shows the observed phase timeline and latest
state per Agent/attempt/component. Started and installed alone never establish
successful execution. Dashboard changes require their own merge and deployment.

## Verification

Run the CLI Go suite, relevant race tests, publication tests and six-platform
builds. Signed fixtures and actual subprocesses cover replacement, checksum and
signature rejection, rollback, cancellation, identity and exit codes, online/offline
observations and TERM/INT stream draining. Release preflight verifies immutable
versioned artifacts and public downloads before advancing the signed latest pointer.
