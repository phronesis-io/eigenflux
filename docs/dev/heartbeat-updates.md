# Heartbeat updates

Native heartbeat installations use one stable launcher with explicit Agent
Home, server and `EIGENFLUX_MODE=skill`. CLI 1.0.0 adds binary updates,
scheduler migration validation, and current-host plugin maintenance receipts.
Native triggers and plugin loops share the same `heartbeat plan` implementation.
Codex uses the agent format; Claude Code and OpenClaw consume the JSON plan and
deliver its `agent_prompt`. Their plan subprocess timeout is five minutes to
allow CLI update attempts and Skills synchronization; ordinary CLI calls keep
their existing timeout. No plugin implements a second updater or update policy.

## Binary updates

Explicit skill and plugin modes enable maintenance. Unknown modes leave it
disabled. CLI automatic updates default to enabled in explicit skill and plugin
modes. The API administrator controls the global ENABLE_CLI_AUTO_UPDATE environment
setting, default true; restart the API after changing it. Each automatic maintenance
entry reads auto_cli_update through authenticated GET /agents/me/settings before
checking the CDN, even within the daily update-check interval. A false policy
returns skipped/server_policy_disabled and records a check/blocked observation.
Ignore historical Home and per-server auto_cli_update values; config set rejects
that key. auto_plugin_update and auto_skill_sync retain their overrides.

Policy requests use a three-second HTTP timeout. Failure logs
update_policy_unavailable and records check/waiting_online; use the last received
policy for the same Home, server, endpoint and Agent, or true if none exists.
Store that policy in the existing runtime sidecar, separate from mutable config
KV. Missing/null fields from older servers mean true. Do not let ordinary settings
sync copy this policy into local KV. The backend switch affects new automatic
maintenance entries, not an in-progress update or an explicit manual update.
Existing CLI releases do not implement this server policy.

Watch checks signed CLI releases independently at startup and rechecks eligibility
each minute, including notification-only mode. It does not invoke an Agent or
require a heartbeat plan. Apply the same backend-controlled update policy.
The existing installation lock and 24-hour throttle are shared
with heartbeat checks and manual `update`; concurrent triggers do not download
the same release independently. Failed or interrupted attempts retry after five
minutes; successful checks keep the 24-hour interval. Failed updates leave business processing active;
successful installation requests the existing drained adoption path. Updater
observations use the existing maintenance queue and redacted `cli_update_status`,
including fixed `reason` and `next_check_at`; unchanged deferred decisions are not
logged again each minute.

Heartbeat plans retain their compatibility check. macOS and Windows use
`cli/latest/package-release.json`; Linux
keeps `cli/latest/release.json` and the existing bare-binary replacement. The
bare manifest retains its original schema/signature so older clients can update.
Both manifests bind stable semantic versions and immutable artifact digests with
the compiled Ed25519 key. A newly observed Skills minimum bypasses the throttle
once; incompatible or untrusted rules never execute.

Complete packages contain the CLI and its paired macOS notification App;
Windows embeds its notification script and icon in the CLI. The updater holds
one OS lock beside the stable executable, validates package and CLI digests,
archive paths, the macOS signature and an exact-version probe under the same
explicit Home. It stages `<entry>.versions/<version>` without replacing running
files, then atomically activates `<entry>.current.json`. Missing packages fail
closed. Partial downloads, invalid signatures and failed probes retain the old
pointer; no bare-binary fallback occurs. Existing version directories are
immutable and verified before reuse. Keep old directories while their processes
may still be running; automatic garbage collection is not part of this change.

Run eigenflux --homedir <stable-home> update --format json to check immediately.
This owner command bypasses the daily throttle and automatic-update policy. Missing or unreadable account
files do not block manual binary updates. It needs no login and reports
the replacement process version, PID and Home. It does not change update switches.
A bootstrap install is required for older CLIs. Installation permission failures
and running-executable replacement restrictions remain explicit failures.

The stable entry forwards commands before parsing to the signed active CLI.
TERM/INT cancels installation before business work. Successful updates re-run the
original arguments with pinned Home, server, Agent, principal and endpoint;
token rotation remains allowed. The child reports its actual version and PID.
Business failures return directly without replaying the task or switching back
to an older executor. Shared throttle/minimum/highest-version state prevents
multiple Homes from independently downloading or downgrading one installation.

Watch checks the active pointer each minute, closes admission, and drains already
admitted PM pulls, tasks and business writes before adopting. Pending journal
entries survive. Unix replaces the drained process with `exec`; Windows uses a
fixed parent/child adoption loop, avoiding running EXE replacement and unbounded
parent chains. Only the identity-pinned watch handoff may request a restart;
ordinary commands never retry on that exit code. Supervised watches emit a
drained `restart_required` for their host to adopt. They are not force-killed.

The new watch's desktop setup updates the shared Mac helper at the existing path
and bundle ID; Windows retains its AUMID and refreshes embedded assets. Existing
notification permissions remain OS-owned. Staging never opens a permission dialog,
changes quarantine or elevates privileges. Mac production packages require
Developer ID signing and notarization; local development may use ad-hoc signing.

Updates require write permission beside the stable entry and an initially
installed package-aware CLI. An already-running older binary must receive one
bootstrap upgrade; new code cannot change its update protocol retroactively.
After a legacy bare update, a same-version CLI installs its paired complete
package, bypassing the previous daily check once; failed checks then obey normal
throttling. A manually replaced newer stable entry never forwards to an older
active pointer.
Development builds without a compiled trust key do not self-update. Heartbeat
maintenance requires explicit `skill` or `plugin` mode. Watch's CLI-only checker
does not require an Agent adapter or infer an unknown host/mode. Plugin mode
never creates a second recurring trigger. Linux keeps its existing
`.previous` backup and installed-probe rollback; an uncatchable termination during
bare replacement remains outside that rollback guarantee.

`auto_plugin_update=false` disables automatic plugin updates; per-server values
override Home-level values. Skills sync retains its configuration and compatibility
checks. The CLI update policy remains backend-controlled.

Update observations carry one attempt ID
through check, download, verify, probe, install, adoption and execute; verified
manifest targets and completed rollback remain separate evidence.

Claude stream shutdown stops socket intake and gives an already-received packet
three seconds to finish cache/output. A second signal or the deadline reports an
incomplete drain; a forced kill remains outside this guarantee.

Settings reports include the effective auto_cli_update boolean as observation
only. The backend stores supplied values as cli_update_policy events in the
existing telemetry_events_v2 table for 30 days. This does not write a mutable
update setting; the GET response independently returns the administrator policy.
Reports share the existing changed-or-daily runtime report cache; failed reports retry without marking that cache successful.

For enabled/disabled shares, select the latest observation per authenticated
Agent in the reporting window, then join recent active Agents. Classify missing
observations as unknown, including older clients that never reported the field.
Count Agents, not requests or update attempts. CLI, host and mode fields permit
cohort comparisons. Deploy the API before releasing the matching CLI; older
servers ignore the new field and cannot establish reporting coverage.

Automatic and manual CLI updates share check, download, verify, probe, install,
installed-path adoption and execute events. One attempt_id crosses the CLI handoff;
the child reports its own executed version. Target versions come only from verified
manifests. Started means phase entry. Rolled_back requires successful restoration
and directory sync. Plugin events distinguish check, verify, install and load;
manager-owned downloads belong to install.

## Native scheduler migration

`heartbeat migrate plan --stdin` consumes a complete, normalized host inventory
and stores at most one one-hour plan snapshot per Home/server/host. Only tasks with
verified heartbeat purpose, EigenFlux ownership, a matching absolute Home and explicit server
qualify. More than one match or missing identity evidence blocks migration.
The plan changes only the prompt; it does not create tasks or write scheduler
databases. Paused tasks remain paused. An already-current task needs no pending
record. Plans retain only the selected task, not unrelated scheduler contents.
Verification leaves this bounded snapshot untouched so concurrent new plans
cannot be deleted. The next update replaces it; expiry prevents stale use.

The host Agent applies the prompt through its native scheduler tool, lists the
tasks again, and submits the fresh inventory to `heartbeat migrate verify
--stdin`. Verification checks the selected task's ID, prompt, cadence, status,
thread and other configuration, and rejects duplicate heartbeat tasks in the
same scope. Unrelated task changes do not invalidate the readback. Only successful
readback writes a host-scoped verified receipt; retries validate fresh inventory against that
receipt while it remains valid. The CLI validates host-supplied evidence;
it cannot independently attest that a host tool was called. Updated Skills
require fresh host reads and prohibit treating stored receipts as current proof.

The installation guide routes existing Agent upgrades to this procedure. The
Feed and Communication Skills invoke it during subsequent native heartbeats.
An old installation that never runs the new installer, CLI or Skills must
receive one bootstrap upgrade before it can participate.

## Plugin maintenance

OpenClaw plans set `plugin_maintenance.executor=cli`. `heartbeat plan` starts
one detached `heartbeat plugin-update --worker` with the same Home, server,
mode, working directory and configured `EIGENFLUX_OPENCLAW_BIN`. The worker
survives the Gateway source-change restart. Launch admission is Home/server
scoped; the update lock lives beside the host plugin registry so different Homes
cannot mutate the same installation concurrently. Disabled/absent plugins and
recorded version pins remain unchanged.

The worker uses the host manager's dry-run resolution to select the latest
compatible release under the recorded selector, including its host and plugin
API constraints. Unrecognized discovery output blocks the attempt. It fetches
the selected exact npm publisher release,
compares every published package file with the installed files, and validates
the recorded package, source, resolved version, integrity and install path.
`provenance-invalid` is an official-catalog classification; third-party npm
records still require publisher artifact verification. Local source selectors,
file changes, inconsistent records, incompatible releases and remote/unmanaged
Gateways remain blocked. The host manager performs updates and owns its managed
restart; the worker waits through delayed source-change restarts, including
retries where the disk version is already current. When the local process still
loads the old version, it may send at most one `plugins.refresh` per attempt to
repair a lost notification. The host owns restart deferral and deduplication.
Verification retains the three-minute deadline.
An unverified target recorded only after a verified installation survives worker
retries. It allows a missing runtime RPC from the old loaded adapter to recover
through one managed refresh; successful adoption clears it. An unchanged older
adapter without this installation evidence remains unverified.

Live `gateway call eigenflux.runtime` is bound to the verified local loopback
service port. It must report the target plugin version, a PID matching fresh
local service status (and a replacement PID after an update), and running/ready watch processes
using the worker's CLI version for every configured account, including the
current server. Early RPC registration and an empty server list are insufficient.
Busy EigenFlux deliveries defer maintenance; ordinary host conversations remain
under the host manager's lifecycle policy. An older adapter without this RPC
cannot attest loading. A three-minute verification timeout retains
`restart_required`. Successful/absent checks are cached for 24 hours; unsuccessful
or interrupted checks retry after five minutes through the watch's maintenance
eligibility check, independently of the daily attempt timestamp. Cached plans say `not_due` and
contain no installed/loaded-version claims. Native maintenance runs no Agent turn.

Codex and Claude Code retain Agent-driven maintenance through their native
managers. Their plans carry the discovery context and require fresh installation
scope and process evidence on each delivered check. `heartbeat plugin-check`
requires installed/latest/loaded versions to agree for `loaded`, and records
unconfirmed adoption as `restart_required`. Missing plugins remain absent;
failed checks retry the following day and disruptive restart remains a host/user
action for these managers. Preserve automatic-update switches in both paths.

## Release and validation

The CLI build generates a legacy `release.json` and a signed complete-package
`package-release.json` after all six binaries and packages exist. Publishing uploads the versioned artifacts before the signed latest
pointer, served with `Cache-Control: no-store`. Versioned objects accept only
identical-byte retries; conditional writes prevent replacement during races.
CLI and Skills workflows share one publication concurrency group. Skills
publication verifies the live signed CLI minimum and all six artifact digests
before writing; successful CLI releases trigger the Skills workflow again.
Publish CLI 1.0.0 before
inviting users to bootstrap; the new Skills require that minimum version.
Merge-to-main Skills publishing alone cannot bootstrap old CLI binaries.

Run `go test ./...` from `cli/`. The automatic-upgrade end-to-end test builds
two real CLI executables, serves a signed release from localhost, and verifies
normal and minimum-version-triggered updates, same-cycle version reporting,
and unchanged Agent credentials. Scheduler tests cover repeated migrations,
paused tasks, duplicate tasks, wrong identities and altered readbacks. Plugin
tests reject unverified load claims. Live host-manager execution and Windows native adoption need platform
integration verification before claiming coverage.


## Bounded plan modes

`heartbeat plan --maintenance-only` selects only the current signed maintenance
reference and returns `execution_order=["maintenance"]`. It uses the same
updater and host plugin contract as the normal plan. Host adapters invoke it
when maintenance is due even when Feed is empty, fails, or polling is disabled.
The host merges same-account concurrent maintenance runs; watch's connections
and runtime lease never wait for the update subprocess.

`heartbeat plan --control-only` reads verified compatible local Skills and
selects only `ef-broadcast/references/commands.md`. It performs no CLI update,
Skills download, plugin check or Feed action. Before onboarding completes its
execution order is empty. Both JSON and Agent output use the same selected
sources and execution order. These flags are mutually exclusive; without either
flag the existing full heartbeat stages remain unchanged.

`auto_skill_sync=false` now also governs automatic `heartbeat plan` and
`skills sync --if-stale` entry points, with server configuration taking priority
over Home configuration. These paths verify signed unchanged compatible local
rules without contacting the CDN. Missing, modified, unsigned or incompatible
rules stop that operation. Explicit `skills sync` remains an owner-requested
sync and does not alter the stored switch.

`heartbeat plan --shell` accepts `posix`, `powershell`, or `cmd`, defaulting to
the platform shell. It uses the same native launcher renderer as migration.
WorkBuddy migration uses its native `automation_update` capability and readback,
preserving ID, cadence, paused state and unrelated task fields.

See [Maintenance observations](maintenance-observability.md) for phase semantics,
identity-scoped offline retries, host receipts, and release queries. Windows complete-package adoption uses immutable version directories rather
than replacing the running EXE; native host integration remains a separate check.


When a plugin's watch supervisor is active, its ordinary Feed plan uses
`heartbeat plan --watch-managed`. The full plan still checks CLI/Skills and
emits business stages, but omits the host-maintenance reference, disables plugin
maintenance instructions and delegates scheduler/plugin maintenance exclusively
to the watch's `--maintenance-only` handler. This prevents concurrent full Feed
and maintenance Agent turns from updating the same host plugin. The flag does
not suppress an explicit maintenance-only or control-only plan. Before watch
activation and with older CLIs, the existing full plan remains unchanged.
Ordinary full plans do not reset the independent maintenance due clock; a Feed
failure therefore cannot indefinitely suppress watch-triggered maintenance.

CLI builds generate checksums for all six platform binaries alongside the signed
release manifest. Publication validates local checksums, preserves conditional
immutable version writes, and verifies both public paths before advancing latest
version and signed release pointers. Skills dispatch follows successful CLI publication.

## Upgrade completion and watch recovery

CLI 1.0.1 restores the current authenticated account's outer loop after manual
updates and automatic maintenance checks. The installer also calls `watch recover`
after host setup. Recovery preserves Home, server, Agent/principal identity and
existing dispatch bindings; it never scans other Homes or rebinds another host.
An existing running owner drains and adopts its update without being restarted.
The watch self-update handoff also refreshes signed host Skills before enabling
dispatch in its replacement process.
A stopped bound watch synchronizes and validates its signed host Skills before
starting the owned OS task. A missing WorkBuddy binding is discovered and verified through its current-host
setup flow. Other missing bindings start notifications only, not model execution. Explicitly disabled OS tasks remain disabled. macOS and Windows use
the same recovery entry; unsupported platforms report a skip.

Recovery reports CLI installation separately from outer-loop status: `ready`
requires a fresh live runtime observation; `starting` is not readiness. Notification
permission and Agent-call verification remain separate fields. Network, rules,
identity or task failures report a bounded reason and are retryable through
`watch recover` or a later automatic maintenance entry. Never reset trust keys,
ignore signatures, or remove user-edited Skills to make recovery succeed.

### Notification initialization is independent of watch recovery

Explicit `update` completion and `watch recover` run the notification Setup
implementation before account, binding, Skills or watch recovery. This also runs
when the CLI is already current and does not require authentication. The macOS
shell installer calls `notify setup --source installer`; the Windows installer
retains its existing setup step. Initialization never sends a test notification.

Automatic authenticated maintenance uses Check, and repairs a missing native
integration with Prepare. Prepare installs the signed macOS helper but performs
only a permission check; it never requests macOS authorization. Windows registration
is silent. Permission-required, denied, setup timeout and unknown-first-notification
states are reported independently in `notifications`, with no visibility claim.
CLI installation success is not notification or watch success. The existing
`EIGENFLUX_SKIP_AGENT_SETUP=1` opt-out also skips notification initialization.
