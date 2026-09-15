# Heartbeat updates

Native heartbeat installations use one stable launcher with explicit Agent
Home, server and `EIGENFLUX_MODE=skill`. CLI 0.0.48 adds binary updates,
scheduler migration validation, and current-host plugin maintenance receipts.
The three plugin packages need no runner changes for this workflow.

## Binary updates

Before resolving a heartbeat plan, the CLI checks `cli/latest/release.json` at
most once per 24 hours. The manifest binds a stable semantic version to each
platform artifact's SHA-256 and size, using domain-separated Ed25519 signatures
and the public key compiled into the CLI. Downloads use immutable versioned
paths. A newly observed Skills minimum bypasses the daily throttle once; an
unresolved minimum retains compatible installed rules. Without a compatible
local bundle, the cycle stops before executing rules it cannot support.

The updater holds an OS lock beside the resolved executable, downloads into
that directory, validates bytes and `version --short`, retains `.previous`,
and replaces the executable. A failed installed-binary probe restores the
backup. A successful install re-runs the original arguments with the same
resolved Home and environment. The new process reports its actual CLI version.
Business errors from the new process are returned without replaying the task.
The update state is binary-scoped so several Homes sharing an installation do
not independently download the same version. It stores last attempt, minimum,
highest accepted version and the most recent error.

Updates require write permission to the installation directory. Network,
signature, permission or replacement failures retain the previous executable
and remain visible in `cli_update`. Platforms that lock running executables,
including Windows configurations rejecting replacement, require an installer
update outside the running process. No elevation or host restart is attempted.
Development builds without a compiled trust key do not update themselves.
Plugin-owned loops skip automatic binary replacement.

`auto_cli_update=false` and `auto_plugin_update=false` disable their respective
automatic operations. Per-server values override Home-level values. Skills
sync continues using its existing configuration and compatibility checks.

## Native scheduler migration

`heartbeat migrate plan --stdin` consumes a complete, normalized host inventory
and writes a one-hour pending plan under the same Home/server. Only tasks with
verified EigenFlux ownership and a matching absolute Home and explicit server
qualify. More than one match or missing identity evidence blocks migration.
The plan changes only the prompt; it does not create tasks or write scheduler
databases. Paused tasks remain paused.

The host Agent applies the prompt through its native scheduler tool, lists the
tasks again, and submits the fresh inventory to `heartbeat migrate verify
--stdin`. Verification checks the task count, IDs, prompt, cadence, status,
thread and other configuration, including unrelated tasks. Only successful
readback writes a verified receipt. The CLI validates host-supplied evidence;
it cannot independently attest that a host tool was called. Updated Skills
require fresh host reads and prohibit treating stored receipts as current proof.

The installation guide routes existing Agent upgrades to this procedure. The
Feed and Communication Skills invoke it during subsequent native heartbeats.
An old installation that never runs the new installer, CLI or Skills must
receive one bootstrap upgrade before it can participate.

## Plugin maintenance

The plan requests a current-host plugin check once daily. The Agent uses the
host's official manager to refresh the trusted source, select the latest
host-compatible release and update an already installed, enabled EigenFlux
plugin. It preserves scope and does not install plugins for other hosts.
Disabled plugins, missing permissions and source mismatches remain blocked.

`heartbeat plugin-check --stdin` records normalized observations from fresh
host reads. `loaded` requires installed/latest/loaded versions to agree;
`restart_required` records installation without claiming active-process
adoption. Missing plugins are recorded without installing them. Failed checks
are retried the following day. Disruptive restart remains a host/user action.

## Release and validation

The CLI build generates `release.json` only after all six platform binaries
exist. Publishing uploads the versioned artifacts before the signed latest
pointer, served with `Cache-Control: no-store`. Publish CLI 0.0.48 before
inviting users to bootstrap; the new Skills require that minimum version.
Merge-to-main Skills publishing alone cannot bootstrap old CLI binaries.

Run `go test ./...` from `cli/`. The automatic-upgrade end-to-end test builds
two real CLI executables, serves a signed release from localhost, and verifies
normal and minimum-version-triggered updates, same-cycle version reporting,
and unchanged Agent credentials. Scheduler tests cover repeated migrations,
paused tasks, duplicate tasks, wrong identities and altered readbacks. Plugin
tests reject unverified load claims. Live host-manager execution and Windows
replacement need platform integration verification before claiming coverage.
