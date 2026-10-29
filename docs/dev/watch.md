# Account watch loop

CLI 1.0.0 adds `eigenflux --homedir <stable-home> --server <server> watch`.
One process owns one canonical Home/server OS lock. Account and principal changes
end that process; credential renewal for the same identity continues normally.
The stream never follows a newly selected default server. Home resolution retains
the existing `.eigenflux` suffix normalization, followed by symlink resolution.

The JSON-lines envelope is `eigenflux_watch.v1`, with `type`, `event_id`,
`state_scope`, and `data`. `state_scope` hashes canonical Home, server, Agent and
principal. Diagnostics contain codes, not tokens or raw remote messages.
The bounded output queue terminates a stalled consumer rather than consuming
unbounded unread messages. PM payloads and friendship types retain the existing
stream format. The legacy `stream` command remains available.

`started` identifies the pinned process but is not a readiness assertion.
`runtime_ready` requires a successful Runtime lease and connected PM socket.
Losing either condition emits `runtime_unavailable`; recovery emits a fresh
`runtime_ready`. Host runtime evidence follows these transitions.
The control SSE is a wake hint; Runtime heartbeat reconciles pending command IDs
and renews at the server's requested cadence (30 seconds currently). Repeated
pending sets trigger at most once per minute; a changed set wakes immediately.
Lease renewals preserve the account's cached applied context revision.
A watch never claims commands.
The host obtains `heartbeat plan --control-only`, then executes the central
owner-command rules. Existing command fencing and two-minute leases apply.

The minute check verifies identity and endpoint, checks Card freshness and
maintenance due state locally, and detects a replaced executable. Card and
maintenance hints are cooled down for five minutes. They are not completion
receipts. The host reuses profile context/refresh-task adapters and the bounded
`heartbeat plan --maintenance-only` pipeline. These calls cannot block socket
keepalive. Feed polling and feedback flushing keep their existing cadence.

CLI 1.0.34 adds an independent update worker in every watch mode, including
notifications-only. Check at startup, then recheck eligibility each minute using
the existing signed-package updater, shared installation lock and daily throttle.
Failed or interrupted checks retry after five minutes; successful checks wait 24 hours.
Honor the pinned server's `auto_cli_update`, then the Home default; do not require
an Agent turn or `heartbeat plan`. Network and package failures keep business
admission open. Hold admission through checking and observation writes, then
request the existing drain on success. Emit redacted `cli_update_status` with the
fixed decision reason and next check time; suppress unchanged deferred decisions. Reuse
the maintenance queue. Heartbeat checks remain a compatible shared entry.

When a signed active package or installed executable reports a newer valid version, watch closes job
admission before claiming further journal entries. Existing PM replies, seller
inspection and paid fulfillment finish normally, including their business writes;
pending entries remain durable. After every active lane finishes, standalone
watch also waits for admitted destructive PM pulls to finish local intake.
New PM pulls and subsequent pages stop when admission closes. Standalone
watch releases its account lock and adopts the installed executable with the
same arguments, canonical Home, server and inherited environment. POSIX replaces
the process in place; Windows uses a bounded adoption parent and immutable
version directories without replacing a running EXE. Identity is pinned across adoption; account or endpoint
changes stop the handoff rather than switching accounts.

Plugins set `EIGENFLUX_WATCH_SUPERVISED=1`. After draining, their watch emits
`restart_required` with `ready: true` and keeps socket reception/leases active while
awaiting the host. It cannot claim more jobs. The existing plugin supervisor
waits for this drained event, stops the old watch, confirms exit, and starts the same arguments using a clean
environment. The supervisor preserves active Agent turns, checks the pinned
account, and waits for a new `runtime_ready`. Binary replacement or `version --short` alone does not
prove runtime adoption. Sharing a binary shares installed versions; per-Home
auto-update switches control initiation, not independent version pinning.

Maintenance reporting and Skills synchronization are shared with native
heartbeats. Native scheduled tasks remain finite `heartbeat plan` calls. Their
migration changes only the prompt through host tools, preserves pause/cadence
and binding, and verifies fresh readback. Legacy missing server fields require
a verified unique binding. POSIX, PowerShell and CMD launchers use distinct
quoting; CMD rejects expansion characters that cannot be represented safely.

The existing PM server marks messages read before delivery. This version does
not promise replay across that failure window. Package updates do not force
host termination. Long control
task renewal, reliable PM history replay, Codex event runners and external
WorkBuddy desktop invocation are separate extensions.

Fatal `identity_changed`, `configuration_changed`, and `owner_replaced`
diagnostics stop the account background runtime. Exit code 78 is authoritative
when the output pipe cannot deliver the final diagnostic. Network failures retain
bounded reconnection. Credential lock waits and refresh requests are cancellable.
Runtime adoption is recorded only after PM and Runtime readiness; a separate,
bounded worker retries queued maintenance observations without blocking leases.

## Restart an existing background watch

CLI 1.0.3 adds `watch restart`. Update the existing task's wrapper/package paths, then invoke the new CLI from
a separate foreground session. Keep its original account, server and environment.
On macOS use the signed-in desktop user, without sudo:

```sh
./eigenflux watch restart --launch-agent "$HOME/Library/LaunchAgents/ai.eigenflux.jax.watch-test.plist"
```

The plist must already exist in that user's LaunchAgents directory. An unloaded
task is loaded; a loaded task is stopped and reloaded to apply changed arguments.
The command preserves its wrapper, including any paired `test-source` startup.
It does not copy a workspace plist or repair its paths automatically.

On Windows, select the exact existing interactive current-user task:

```powershell
.\Run-Test.ps1 watch restart --scheduled-task '\EigenFlux\watch-test'
```

Neither command creates a service, enables a disabled task, or selects an account
from CLI Home/server flags. Use the actual task path for the intended account.
`process_started` confirms three seconds of stable OS process/instance state;
`runtime_verified:false` requires checking fresh `runtime_ready` in watch logs
with the expected `cli_commit` and new watch `pid` (a wrapper PID may differ),
and an actual message separately. Notification `ready` is a separate check.
Reload/start errors return nonzero; inspect the task's existing stderr log.

For source installation, `bash cli/scripts/install-local.sh --watch-launch-agent
<absolute-plist-path>` calls the same restart after updating CLI/Skills, even if
notification permission is denied. Without this option, installation still
requires explicit restart. A stopped process cannot recover its own unloaded
OS task; the upgrade procedure must invoke this step.

## Desktop order notifications

CLI 1.0.11 supports `watch --notifications-only` without a host adapter or
execution binding. It polls order notifications once immediately and every
minute, saves the desktop queue before ACK, and never consumes PMs or runs an
Agent. It does not advertise Agent Runtime readiness. A successful poll emits
`notifications_ready`; OS submission is reported separately with visibility
unknown. The saved account ownership keeps legacy stream from consuming orders.

On Windows, `eigenflux --homedir <stable-home> --server <server> watch start`
creates and starts a non-elevated interactive current-user logon task. Installer
calls it after silent notification setup. Before authentication it reports
`pending_auth`; onboarding calls it again after authentication. An existing
execution binding reports `host_managed` and must retain its host-owned watch.
Task name and logs use `EigenFlux-Notifications-<account-scope>` under Home's
`watch/` directory. Disabled tasks are preserved. Verify fresh
`notifications_ready` with the expected PID/commit, not just `process_started`.
No automatic test toast is sent. Previously ACKed events are not replayed.

On macOS and Windows, `watch --dispatch` enables desktop notifications for
`commission_order` subscribers by default. Buyer and seller accounts receive
their own order stages and reminders. Notifications do not invoke an Agent or
inject work into the active conversation. Use `--desktop-notifications=false`
to disable this sink without changing order processing.

Desktop titles and bodies follow the current user's first OS UI language.
Chinese variants use Simplified Chinese; other languages or detection failures
use English. Restart watch after changing OS language. Already queued messages
keep their text; OS-owned permission dialogs follow the OS independently.

`notify setup` installs the per-user integration and requests OS permission;
`notify status` checks it, and `notify test --url <https-url>` checks submission
and click-through. macOS requires the helper app included in the CLI bundle.
For a self-built Mac test package, keep `EigenFlux Notifications.app` beside the
real executable and run `./eigenflux notify setup`, then `./eigenflux notify status`
from that directory. `cli/scripts/install-local.sh` performs helper installation
and setup automatically. macOS prompts only on the first request; a previous
denial requires System Settings > Notifications > EigenFlux. After updating,
a running watch adopts a newer installed CLI after active jobs finish. Same-version
test replacements, helper-only changes and new subscriptions require an explicit
restart using the same Home/server and dispatch arguments. Updating CLI/Skills
does not enable an order subscription.

Setup failures emit `desktop_notification_status` with fixed `code` and `action`:
`helper_missing` / `install_complete_bundle`, `permission_required` /
`run_notify_setup`, `permission_denied` / `open_system_notification_settings`,
`setup_timeout` / `check_system_permission_dialog`, `unsupported_platform` /
`use_macos_or_windows`, or `setup_failed` / `run_notify_setup` for direct diagnosis.
Watch retries failed setup and OS submission separately from Agent execution.
Permission denial does not stop order processing; pending notifications remain
on disk. Corrupt/full queues or failed durable writes stop watch instead of
acknowledging notifications that were not safely recorded.

Socket and HTTP intake persist execution and desktop queues before remote ACK.
Desktop receipts deduplicate source notification IDs across restart/rebind;
local unresolved results are recovered from the execution journal. The worker
submits at most one notification per three-second cycle. OS submission does not
confirm banner visibility, which depends on system settings and Focus mode.

Clicks open the configured Console's notification route with account, order,
and role identifiers, never credentials. The Console requires the target
account before fetching order details; another logged-in account must be
explicitly switched. HTTPS and HTTP on `localhost`, `127.0.0.1`, or `::1` are
supported; other origins disable desktop delivery with
`unsupported_console_origin` while watch continues.
Implementation and maintenance rules: [desktop queue](../../cli/internal/desktopqueue/README.md).

TODO: route future Agent-to-Agent delegation availability through the same
account-scoped watch after its authorization, claim/cancel and result protocol
is specified. No delegation capability or A2A handler is registered now.

## Commission diagnostics

Commission dispatch reuses the account-scoped maintenance queue and batch API.
Execution, delivery substeps, retry schedules and main-conversation receipts
follow [the observability contract](commission-observability.md). Diagnostic
failures never repeat a business operation. Keep that contract synchronized
when changing these stages.
