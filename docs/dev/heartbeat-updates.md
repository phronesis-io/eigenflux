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

Before resolving a heartbeat plan, the CLI checks `cli/latest/release.json` at
most once per 24 hours. The manifest binds a stable semantic version to each
platform artifact's SHA-256 and size, using domain-separated Ed25519 signatures
and the public key compiled into the CLI. Downloads use immutable versioned
paths. A newly observed Skills minimum bypasses the daily throttle once; an
unresolved minimum retains compatible installed rules. Without a compatible
local bundle, the cycle stops before executing rules it cannot support.

The updater holds an OS lock beside the resolved executable, downloads into
that directory, validates bytes and `version --short` under the same explicit
stable Home, retains `.previous`,
and replaces the executable. A failed installed-binary probe restores the
backup. TERM/INT during the update cancels the probe, waits for rollback and
stops the cycle before business work. A successful install re-runs the original arguments with the same
resolved Home and environment. The new process reports its actual CLI version.
Business errors from the new process are returned without replaying the task.
During re-execution, termination signals are forwarded and the child is reaped;
an unresponsive child is killed after a bounded grace period. Signal exits use
the conventional `128 + signal` status; normal child exit codes are retained.
The update state is binary-scoped so several Homes sharing an installation do
not independently download the same version. It stores last attempt, minimum,
highest accepted version and the most recent error.

Updates require write permission to the installation directory. Network,
signature, permission or replacement failures retain the previous executable
and remain visible in `cli_update`. Platforms that lock running executables,
including Windows configurations rejecting replacement, require an installer
update outside the running process. No elevation or host restart is attempted.
Development builds without a compiled trust key do not update themselves.
Explicit `skill` and `plugin` modes share automatic binary and plugin maintenance.
Unknown modes do not enable automatic maintenance. Plugin mode continues to
skip native scheduler migration and never creates a second recurring trigger.
An uncatchable process termination during replacement can interrupt rollback;
the retained backup is not an automatic crash-recovery launcher.

`auto_cli_update=false` and `auto_plugin_update=false` disable their respective
automatic operations. Per-server values override Home-level values. Skills
sync continues using its existing configuration and compatibility checks.

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

The CLI build generates `release.json` only after all six platform binaries
exist. Publishing uploads the versioned artifacts before the signed latest
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
tests reject unverified load claims. Live host-manager execution and Windows
replacement need platform integration verification before claiming coverage.


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
identity-scoped offline retries, host receipts, and release queries. Windows
running-file replacement remains an accurately reported blocked outcome when
not supported; this release does not add an exit-after-replacement helper.


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
