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

The plan requests release discovery once daily per current workspace context.
The Agent freshly inspects installation scope and process-load evidence on each
delivered heartbeat; cached receipts never attest current installation or loading.
Due maintenance sets `wake_on_empty` so a plugin delivers the plan even when
Feed is empty; onboarding business restrictions remain unchanged.
Scope changes force fresh release discovery. The Agent uses the
host's official manager to refresh the trusted source, select the latest
host-compatible release and update an already installed, enabled EigenFlux
plugin. It preserves scope and does not install plugins for other hosts.
Disabled plugins, missing permissions and source mismatches remain blocked.
The plan carries its discovery `context` into the receipt, so a plugin fetching
the plan and an Agent recording results from another directory share the same
daily check without mixing unrelated workspaces.

`heartbeat plugin-check --stdin` records normalized observations from fresh
host reads. `loaded` requires installed/latest/loaded versions to agree;
`restart_required` records installation without claiming active-process
adoption. Receipts include the host-verified installation scope. Cached plan
output retains only release-discovery information and marks `check_required`.
Missing plugins are recorded without installing them. Failed checks
are retried the following day. Disruptive restart remains a host/user action.
Managers that cannot stage updates without interrupting the running plugin
must defer installation to an authorized maintenance window.

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
