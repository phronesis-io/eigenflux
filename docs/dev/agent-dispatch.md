# Local Agent Dispatch

`watch --dispatch` sends account events to a bound local Agent; the CLI delivers replies.
Read and update the [implementation map](../../cli/internal/dispatch/README.md) with every logic change.

## Automatic Windows WorkBuddy setup

CLI 1.0.24: run `watch start --host workbuddy` with the existing explicit
Home/server and signed-in WorkBuddy. Discovery, host probe, account binding and
owned task installation are automatic. See [native acceptance](windows-workbuddy-acceptance.md).
Windows remains pending native acceptance; protocol readiness alone proves
neither model/tool execution nor delivery.

## Configure and run

Authenticate/onboard the intended EigenFlux account and log in to the host first. Bind from that account's Home/server. The CLI pins identity; resolve outstanding jobs before rebinding. An explicit native Codex main session reuses its desktop conversation; other native entries retain their host-specific sessions.

Write a binding JSON file using these fields:

| Field | Value |
|---|---|
| `mode` | `native` (default), `acp`, or `command` |
| `host` | Native: `codex`, `claude-code`, `openclaw`, `hermes`; custom label for ACP/command |
| `command` | Executable + fixed prefix arguments as an array; prefer an absolute executable path |
| `args` | Additional argument array, before native adapter arguments |
| `main_session_id` | Exact existing main-owner session ID; Codex requires its UUID and pinned `CODEX_HOME` |
| `main_command` | Optional complete ACP bridge argv for main notices only; retain the ordinary execution `command`/`mode` for PM decisions, input checks and fulfillment |
| `workdir` | Existing absolute workspace path |
| `skills_dir` | Signed compatible Skills directory containing the dispatch rule |
| `env` | Host environment; `EIGENFLUX_*` keys forbidden; Windows keys ignore case and must be unique |
| `host_agent` | Required OpenClaw Agent selector |
| `timeout_seconds` | Default 600; range 1–3600 |
| `events` | Default `["pm_push"]`; optional `profile_review_due`, `maintenance_due`, `control_pending` |

```json
{
  "host": "claude-code",
  "command": ["/absolute/path/to/claude"],
  "workdir": "/absolute/path/to/workspace",
  "skills_dir": "/absolute/path/to/signed-skills"
}
```

```sh
eigenflux watch bind --config /absolute/path/to/binding.json
eigenflux watch doctor
eigenflux watch --dispatch
```

`doctor` checks configuration only; `status` shows redacted execution state. Neither proves live Agent execution. CLI checks fresh `auto_reply_pm` before invoking the Agent and before sending; disabled permission yields `needs_user`.

## Codex main conversation

CLI 1.0.5 binds a new native Codex PM dispatcher to a verified heartbeat target
and pins the host's absolute Codex store. Submit the host's complete scheduler
inventory with the new configuration, or provide an explicit `main_session_id`.
An absent watch binding does not establish first-time EigenFlux onboarding.
Other host adapters retain their existing routing.

Existing Codex bindings expose `bootstrap_required` in the heartbeat plan.
CLI 1.0.10 prepends `main_session_bootstrap` to full native Codex Skill cycles
after onboarding, including the plan run at upgrade completion. The host
automatically completes the signed initial-binding procedure before business
stages; existing valid targets are left unchanged. Narrow watch-owned maintenance
and control handlers never execute this stage or stop their own parent.
Use the host's native scheduler API to read a complete ownership inventory;
run `watch bind --preflight` through `--scheduler-stdin` or `--scheduler-file`
while the existing watch is running. After preflight succeeds, stop it, reread
the native scheduler and apply with the returned task/thread/session/revision
guards. New creation adds `--config` and uses empty expected session/revision.
CLI accepts only one active EigenFlux heartbeat for the bound Home/server
whose `thread_id` resolves in the pinned Codex store. Restart the same watch
entry after readback. A failed commit with unchanged original routing must
restart the previously running entry before reporting; inactive and disabled
entries remain inactive. Native target readback belongs to the host adapter.
Paused historical tasks do not compete with the unique active target. No local
task filename or conversation title is fixed.

An established target changes only through owner-directed
`watch bind --current-session` or `--main-session-id`, carrying the old target
and binding revision. A changed scheduler target cannot replace it implicitly.
Missing or archived targets require owner action. CLI 1.0.12 queues PM work into
the existing Codex host with `codex queue --thread`, preserving the desktop
writer. It reads new host rollout receipts and requires the exact submitted
input, matching thread/turn, final answer and turn completion before sending.
Queue admission alone never proves execution or delivery. The host must expose
the supported append-only rollout format; unsupported formats fail closed.
Timeout or cancellation leaves an uncertain job for explicit reconciliation;
it does not cancel a turn already queued in the host. Never automatically replay
these jobs. `dispatch_decision` records job ID, session, host turn and action;
`dispatch_status` records the send receipt. Automatic successor migration and
Windows desktop lifecycle remain unverified.
See the synchronized [main-session procedure](../../skills/ef-communication/references/main-session.md).

ACP requires v1 newline-delimited JSON-RPC over stdio; permission requests yield `needs_user`. Command mode reads the prompt from stdin and returns the [decision contract](../../skills/ef-communication/references/dispatch.md) on stdout. Use fixed argv, not a shell string. Windows `.cmd`, `.bat`, `.ps1` executables require an explicit interpreter/entry point. Oversized native command lines yield `needs_user`; use ACP/command stdin for large prompts. OpenClaw uses `--local` with isolated sessions: stop the Gateway sharing its state directory or configure a separate local state directory. If an earlier dispatch build used OpenClaw, resolve outstanding jobs and rebind before restarting to discard shared main-session references.

## Ownership and recovery

Stop the old account's plugin/message consumer before starting dispatch. Keep the companion `heartbeat plan --watch-managed`: it skips subscribed work and retains maintenance unless `maintenance_due` is enabled. Restart a stopped watch to resume its ownership; bindings do not automatically fall back to another consumer.

Stop watch before mutating bind/retry/reconcile; read-only `watch bind --preflight` keeps it running. Retry only `failed` or `needs_user`; inspect host/business results before reconciling `unknown/failed/needs_user` work:

```sh
eigenflux watch status
eigenflux watch retry JOB_ID
eigenflux watch reconcile JOB_ID --outcome replied --reply-id VERIFIED_MESSAGE_ID --verified
eigenflux watch reconcile JOB_ID --outcome no_reply --verified
# Optional events in unknown/failed/needs_user:
eigenflux watch reconcile JOB_ID --outcome completed --verified
eigenflux watch reconcile JOB_ID --outcome failed --verified
```

Reconciliation records the operator's conclusion, including PMs already handled manually. Explicit profile retry bypasses its prior claim cooldown; ordinary scheduled reviews keep that cooldown. Legacy `unknown` work is never automatically resent; verified fulfillment delivery retries follow the bounded recovery below. Optional events without completion evidence remain `needs_user`; inspect before retry. Reconcile as `failed` only after confirming work did not complete; retry stays explicit. Legacy `accepted` records are unverified.

CLI 1.0.27 retries recoverable delivery failures at most five times in total,
including the first attempt, after 1/2/5/10 minutes. A durable delivery record keeps
the original artifacts, attempt count, due time, keys and delivery body. Retries
never rerun the Agent. Waiting jobs do not block other orders. Uncertain writes
first require order readback and bounded download/hash verification; matching
completed delivery finishes without another submission. Only unchanged work may
resume under the same keys. Identity/configuration/input/output changes, conflicting
remote outputs, denied permissions and cancelled/refunded Orders stop retries.
Expired upload grants never cause a fresh-key upload.

Stop watch before explicit recovery after exhaustion. Pre-upload checkpoints can
reuse verified files on explicit job retry. Legacy uncertain work without delivery
retry metadata requires operator reconciliation; loose files do not establish a
checkpoint or authorize automatic replay.

WorkBuddy Commission runs through the current CLI-managed ACP bridge grant
read-only access to the frozen contract's validated Skill directory. Existing
workspace permissions stay unchanged. Keep the bridge executable paired with
the running CLI; custom ACP commands keep their explicit permission policy.
Denied access outside the bound Skill still stops work. Recover a previously
blocked intake by job ID after stopping its owned watch; retain the binding
revision, account and queue.

## Build isolated test packages

Use `codex/heartbeat-autoupdate`, the Go version in `cli/go.mod`, and a fresh output directory. Packages contain paired verification keys and signed Skills; keep them separate from production installs. Run CLI and Agent as the same ordinary user.

Windows, from the repository root:

```powershell
.\cli\scripts\build.ps1 -TestBundle -Arch amd64 -OutDir build\dispatch-windows
cd build\dispatch-windows
.\Run-Test.ps1 version
.\Run-Test.ps1 skills sync
```

Use `arm64` on native ARM64 Windows. Authenticate a test identity and run the watch commands through `Run-Test.ps1`; bind the absolute `.test-state/skills` path.

macOS, from `cli/`:

```sh
go run ./cmd/devbuild -test-bundle -target darwin/arm64 -out build/dispatch-mac
```

Use `darwin/amd64` on Intel. Start the package's `test-source -serve` against its `cdn` directory. Set package-local `EIGENFLUX_HOME`, `EIGENFLUX_SKILLS_DIR`, and the printed `EIGENFLUX_CDN_URL` for CLI calls. `Run-Test.ps1` is Windows-only.

Keep `EigenFlux Notifications.app` beside the Mac CLI. In the package directory,
run `./eigenflux notify setup` and `./eigenflux notify status` before testing order
notifications. Packaging and Skills sync do not request permission. A running
watch adopts a newer installed CLI after all active jobs finish.
Same-version test replacements and helper-only updates require an explicit
restart with the same Home/server and dispatch arguments. If
previously denied, enable EigenFlux in System Settings > Notifications.
When replacing a package used by a background watch, update that task's wrapper
path and finish with `watch restart --launch-agent <plist>` on macOS or
`watch restart --scheduled-task <task-path>` on Windows. Preserve its account
environment and paired local source; verify fresh `runtime_ready` after restart.

## Acceptance and limits

For each OS/host, record version, account, workspace, message/session IDs and reply receipt. Use consenting test identities to verify:

1. Incoming PM starts an Agent turn and produces a reply without manual prompting.
2. Duplicate WS/poll delivery produces one reply; socket outage allows fallback intake.
   Two distinct incoming messages with identical reply text use different `request_id` values and receive distinct send receipts. Retrying the same request ID within 60 seconds reuses its first receipt after the updated gateway and PM RPC are deployed.
3. Two accounts stay isolated; switching during history/model work prevents the old reply.
4. Permission revocation, denied host permission and send timeout yield `needs_user`/`unknown`, without automatic resend.
5. Restart/retry/reconcile preserve recorded outcomes; test spaces in paths, missing executables, workspace permissions, descendant cancellation and Windows shims.
6. Explicit profile/control/maintenance subscriptions produce the actual Card/command/receipt; process exit alone fails acceptance. Check migration preserves `--watch-managed` and non-PM bindings leave messages unread.

Server fetch marks messages read before local persistence: crashes/write failures in that window can lose jobs. Exactly-once/lossless delivery is not guaranteed.

WorkBuddy local owner access remains unverified; no native hidden-main bridge or cloud API integration. Its explicit ACP main binding can negotiate the hidden-main extension, but ordinary CodeBuddy ACP or Channels do not grant it. Task delegation is TODO. Fixture tests and cross-compilation do not verify live models, native Windows behavior or Computer Use.

## Reception diagnostics and prompt cost

`pm_push.data.transport` identifies `socket` or `http_poll`; `received_at` is the local UTC emission time after intake persistence. Match message IDs to `watch status` and its reply receipt. Duplicate receptions may appear from both transports while producing one job. Empty polls are reception events, not Agent turns.

For small PMs, use a dedicated workspace with concise owner instructions. Host-wide Skills, plugins, MCP tool schemas and retained sessions can dominate input tokens; inspect the actual host transcript before trimming message history. Keep identity, signed dispatch rules, authorization and relevant conversation context. Do not globally disable user rules or change the model as an implicit optimization. Consider an explicitly configured minimal host profile only after validating that authentication, permissions and dispatch behavior remain intact.

## Commission integration status

CLI 1.0.13 reports fixed seller execution issue categories to the Commission
execution-report API. Deploy that API and its migration first. The independent
worker retries observations without repeating execution, keeps the captured
order/execution revision fence, and never switches account or refreshes a stale
report fence. Console shows the current version's issue in order list/detail;
buyers see only a generic blocker. Recovery or a later transaction stage removes
obsolete prompts. Existing jobs without a captured fence need an explicit safe
retry; upgrading does not infer or replay historical work. Reports and OS
submission receipts do not prove that a user saw a notification. PM processing
does not produce desktop notifications.

The explicit `commission_order` subscription enables durable notification intake and a separate seller input-check worker. It reads the frozen contract and bound Skill, verifies downloaded inputs, invokes a fresh Agent session and saves a strict `commission_result` in `watch status`. Files are capped at 128 / 64 MiB; preparation and execution have a local 180-second ceiling after queue claim. Paid `ready` results atomically queue a separate local fulfillment worker. It saves output paths before execution, verifies real files and retains them with hashes and self-check evidence in `commission_fulfillment`. Awaiting-seller `ready` invokes seller-authenticated acceptance; unavailable Agents leave orders waiting. Paid `artifacts_ready` persists evidence before fixed-key uploads and delivery, then reads back the order before confirming submission. Recoverable deliveries retain evidence and follow the bounded idempotent retry schedule; legacy uncertain mutations require reconciliation. Buyer confirmation remains separate. Local readiness or artifacts alone are not delivery receipts.

PM + Commission subscriptions share the Socket. A Commission-only subscription uses an initial HTTP request and 60-second reconciliation, because the current server Socket always consumes PM. The same account's ordinary stream skips Commission notification draining while watch owns it. Stop and inspect unresolved jobs before rebinding. The implementation and pending business decisions are in [Commission dispatch](commission-dispatch.md).

### Missed seller order recovery

Stop watch and run `watch retry --order-id ORDER_ID` with the bound Home and server to persist one seller intake when notifications were missed or previously read. The authoritative order must belong to the current seller and be awaiting seller acceptance or in progress. The binding must subscribe to `commission_order`; status and doctor expose its subscription independently of PM readiness. Start `watch --dispatch` afterward to execute the queued intake. Existing fulfillment records and uncertain intake jobs require the existing job-ID retry/reconcile workflow. This command neither creates notification records nor acknowledges the inbox.

## Test listing publish verification

Use the same Commission execution binding for listing trials and paid fulfillment.
`commission publish` requires `--trial-input` and, for file-based services,
`--trial-materials`. It runs the bound background Agent and checks real outputs;
fix reported blockers before retrying. Inspect retained trial files after a timeout
or unconfirmed result. Passing a sample does not replace per-order input checks.

## Test main conversation transaction notices

See commission-main-conversation.md for role/stage differences and limits.
Codex macOS main notices use the existing Desktop owner through its pinned local
IPC socket; CLI 1.0.18 enables the tested hidden-input adapter. No writer is
resumed or replaced. Signed rules enter application context and event metadata
remains untrusted; input is empty and only the final result appears in the main
conversation. CLI 1.0.21 implements the Windows Desktop local named pipe and
enables exact native Codex test bindings. Status remains implemented_untested;
real Windows acceptance is pending. CLI 1.0.22 adds explicit ACP main targets and
the negotiated [hidden-main bridge](acp-main-conversation.md). Preserve ordinary
execution and configure its result-only bridge in `main_command`; a main-only
bridge cannot replace a fresh-session execution adapter. Claude Code/OpenClaw
need their updated local plugins; Hermes uses `watch acp-bridge --host hermes`
with explicit `--owner-url`, `--main-session-id`, `--workdir` and optional
`--token-file`. These three are experimental and untested with real hosts.
WorkBuddy has generic binding/negotiation only; its native hidden-owner entry
remains unavailable. Missing capability sends no business prompt. Neither
standard ACP session/load nor Channels is a hidden-main fallback.

watch doctor --probe-main only discovers the exact owner. Its owner_ready result
is connectivity, not idle status or business completion. watch status separates
enabled from business_verified. With OS notification permission enabled, busy and
proven pre-submission failures use the same durable notification ID for at most
five total attempts, with 1/2/5/10-minute delays. Pending schedules survive restart;
success, revoked permission or a changed Order stops retries. Submitted uncertain
calls are never replayed. No notice targets another conversation. The controlled busy test passed, but Desktop
inject/start is not an atomic idle-only API. A started turn may finish after the
caller times out; do not promise rollback.

The 2026-10-20 real Go test completed in 3.614 seconds with a matching final answer
and no user message. The owner separately confirmed the hidden-input UI. These
are main-thread integration tests with local fixture data, not live payment or
delivery acceptance. Publisher permission warnings and normal-cycle buyer
confirmation reminders retain their existing behavior.

WorkBuddy publication approval also authorizes configuring its missing local
background entry for the same account: macOS native CLI `watch workbuddy-acp`
with an absolute Node path, `commission_order`, and an owned user-level
`watch --dispatch` service. Use writable application data outside protected
Documents. Preserve existing jobs and scoped host permissions; no main-session
binding or GUI refresh is required. Publish still requires a real successful trial.

### CLI 1.0.19 business fixes

Use matched CLI/Skills 1.0.19 or newer. Before creating and again before publishing, run notify status
--publish-preflight --format json separately and display its warning/settings
link before using existing approval or requesting it. Create and publish keep stderr
guidance as fallback; this adds no mandatory permission gate. For confirmed trial
failures, the Agent repairs authorized local execution or Skill defects and reruns
the full trial. Unknown executions and uncertain publication responses require
inspection before retry; contract terms and host security remain unchanged.

Buyer clarification reads verified uploaded Order inputs, including request.txt,
through a buyer-only manifest/grant boundary. CLI 1.0.20 escalates unresolved
facts/files to the buyer and links the account-protected Order page. Staged
add/replace uploads become authoritative only through the atomic supplement,
which wakes seller reinspection. Deploy Commission migrations 15/16 and the BFF;
verify object-store browser PUT CORS. Explicit main-notice queue overflow skips only that auxiliary
notice. Existing local seller blockers notify the bound main conversation once
per job/order-version/status/code, without restarting work; stale or resolved blockers are
skipped. Full Feed contracts now preserve pending-confirmation reminders even
when no Feed item warrants display.

### Windows main-conversation acceptance

CLI 1.0.21 uses the existing Desktop owner at its fixed local named pipe, checks
current Windows user/session identity, then reuses the exact-owner hidden turn
and read-only receipts. No administrator permission or new host daemon is needed.
Keep CODEX_HOME and the main session bound to the same local store. watch status
shows implemented_untested; watch doctor --probe-main reports owner reachability,
implementation_status and host_verified independently. A ready probe starts no
turn and does not prove hidden UI or business delivery.

On the later Windows machine, install the complete test package, retain the exact
binding, probe it, then verify one controlled main result without a prompt bubble,
busy-thread rejection and real buyer payment/confirmation notices. Do not repeat
an uncertain turn or select a different conversation. This change does not run
that acceptance or install/restart the local package.

## WorkBuddy host execution bridge (experimental macOS)

Bind ordinary execution as `mode: acp`, `host: workbuddy`, with command argv
`["/absolute/path/to/eigenflux", "watch", "workbuddy-acp"]`. Keep the account's
Home, server, signed Skills and WorkDir pinned through the existing binding.
The CLI discovers the unique same-user native Sidecar and asks it to start a
fresh isolated worker with WorkBuddy's native child bootstrap and product config.
It does not copy Desktop credentials, start a standalone unauthenticated CLI,
or use a cloud assistant API. WorkBuddy must already be running and logged in.
`--allow-tool` supplies explicit host tool rules; no automatic approval is added.
Unknown or denied tool permission remains `needs_user` through the ACP runner.

This entry is for seller inspection, paid fulfillment and other ordinary runner
work. It does not advertise the hidden-main extension or establish original
Desktop conversation delivery. WorkBuddy main notices remain unavailable without
a separately verified owner bridge. Windows is unsupported by this entry.

For real WorkBuddy model acceptance against isolated order APIs, build the CLI
and run `EIGENFLUX_WORKBUDDY_SMOKE=1 EIGENFLUX_WORKBUDDY_SMOKE_CLI=/absolute/path/to/built/eigenflux go test ./cmd -run '^TestCommissionWorkBuddyHostLocalFulfillment$' -count=1 -v -timeout=8m`
from `cli/`. The smoke explicitly allows filesystem/shell tools in its disposable
workspace, verifies automatic acceptance, exact output bytes and confirmed
upload/delivery, and simulates the paid transition. It never pays or changes a
live order. Real transaction and Desktop UI acceptance remain separate checks.
