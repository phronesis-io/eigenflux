# Windows WorkBuddy acceptance (CLI 1.0.24)

Target: `test/commission-outer-loop-20261009`. Do not use a production release
or mix an executable with another checkout's Skills. Build/install the paired
test bundle through `cli/cmd/devbuild`; preserve the same Agent Home and server.

1. Keep WorkBuddy open and signed in. Run `eigenflux version`; record version,
   commit, executable path and Home. Synchronize the bundle's signed Skills.
2. Run `eigenflux --homedir "<existing-home>" --server eigenflux watch start --host workbuddy`.
   No binding JSON or manual executable configuration is needed. Require
   `host_protocol_verified:true`, `mode:dispatch`; these do not prove a model call.
3. Run `watch doctor` and `watch status` using the same Home/server. Require
   `host:workbuddy`, `commission_order` subscription, a running runtime pulse,
   and fresh `runtime_ready` with the installed commit/PID in the task log.
   Notification readiness must be checked separately. Capture errors unchanged.
4. With no active order work, terminate only the watch child identified by the
   fresh status PID. Leave its PowerShell supervisor and WorkBuddy running.
   Require a different PID and fresh `runtime_ready` within 60 seconds; record
   `watch_process_exit` and its exit code. Repeat a user logout/login separately
   to verify the logon trigger. Do not terminate a model performing paid work.
5. Use an explicitly approved 0.10 CNY test listing. Complete its required
   publish trial; a protocol probe cannot replace this model/tool/artifact check.
   From a second account create an order with complete materials. Require seller
   automatic inspection and acceptance without manually opening the seller
   conversation. After the human confirms payment, require automatic fulfillment,
   verified output artifacts and buyer-side pending-confirmation state. Record
   both Agent IDs, order ID, timestamps, job results and task commit.
6. Confirm a model result marks `runtime.agent_call_verified:true`; this alone
   does not prove the order was accepted/delivered. Verify those server states
   separately. Do not confirm delivery, issue refunds, or change bindings on
   behalf of the human during diagnostics.

Failures: stop at the failed stage; report the exact redacted CLI/host error,
status and task log. Never fall back to an independently logged-in ACP process,
create another EigenFlux account/Home or fabricate successful verification.

## Review follow-up (CLI 1.0.25)

Notification-to-dispatch migration reconciles previously ACKed seller orders
once per binding revision. Transient lookup failures emit migration-pending
status and retry once a minute without stopping reception. Existing jobs are
not automatically replayed. A historical paid order without pre-acceptance
material-check evidence remains blocked rather than fabricating that evidence.

An already-running execution watch is reused only for an identical binding.
When changing CLI/Skills package paths, a safe maintenance stop is still
required before rerunning automatic setup; do not force-stop active paid work
or report the refused upgrade as complete. A notification-only task can still
migrate automatically. Exit 78 is not retried inside the wrapper but may cause
bounded outer scheduler retries. Native Windows acceptance remains pending.
