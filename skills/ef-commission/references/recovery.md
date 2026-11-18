# Recover Local Order Execution

Apply outside CLI-dispatched business invocations. Diagnose interruptions automatically. Resume work covered by the owner's request or subscribed seller contract; retain the Mutation Protocol for effects outside that authorization.

## Diagnose

1. Resolve the Order's existing Agent Home, account, server, host binding and OS watch task. Preserve the same `--homedir`, `--server` and `--format json` on account-scoped commands.
2. Read `eigenflux version`, `watch status`, `watch doctor`, `order get ORDER_ID` and `order files ORDER_ID`. Match jobs by Order ID and stage; inspect execution diagnostics, session results, local artifacts and submitted snapshots. Check each command's `--help` when capability differs.
3. Resolve `skills path`; verify the frozen `fulfillment_skill` file, frontmatter name and readable contents. Restore a known matching skill or correct a verified host target before retrying. Route expired authentication through `ef-profile`; report unresolved binding, permission or skill requirements precisely.
4. Determine whether the interruption affects watch startup, notification intake, inspection, local generation, upload, delivery or main-conversation notification. Verify server effects before changing the journal.

## Choose the Recovery

Apply authoritative lifecycle and business waits first; resolve uncertain execution effects before selecting a retry.

| Verified condition | Required action |
|---|---|
| CLI upgrade broke watch startup or left its installed task stale | Run `eigenflux watch recover`; preserve the binding and disabled tasks. |
| An existing enabled OS watch task needs a process restart | Run `eigenflux watch restart` with its verified `--launch-agent` or `--scheduled-task` target. The task itself selects the account and environment. |
| Matching job is `failed` or `needs_user`, and its blocker is resolved | Run `eigenflux watch retry JOB_ID` once. |
| Seller notification intake is absent and the current Order is `awaiting_seller` or `in_progress` | Run `eigenflux watch retry --order-id ORDER_ID` once; require the existing `commission_order` subscription. |
| Matching job is `unknown`, or execution may already have produced external effects | Establish the actual stage outcome, then apply reconciliation below. |
| Matching job is pending or running, or CLI has scheduled a bounded delivery retry | Allow the existing execution to proceed; inspect runtime health and recorded retry progress. |
| Order is awaiting buyer input, payment, platform validation or buyer confirmation | Follow the business wait below. |
| Order is cancelled, refunded, refund-pending or completed | Follow its authoritative lifecycle state; retain local execution evidence. |

`recover` repairs watch infrastructure. `retry JOB_ID` requeues a failed or blocked job. `retry --order-id` records missing intake and preserves existing jobs. `reconcile` records an evidenced outcome; execution requires a subsequent eligible retry.

## Reconcile Uncertain Work

Inspect the Agent session, actual artifacts, workspace snapshots and Order history. Match evidence to the interrupted job's stage.

- Confirmed stage completion: run `eigenflux watch reconcile JOB_ID --outcome completed --verified`; continue only the remaining authorized stages. A completed intake or generation job establishes that stage only.
- Confirmed incomplete stage with external effects fully accounted for: preserve verified artifacts and checkpoints, run `eigenflux watch reconcile JOB_ID --outcome failed --verified`, then run `eigenflux watch retry JOB_ID` once after resolving the cause.
- Unresolved execution or submission outcome: retain `unknown`, report the missing evidence and stop replay. Use the Order workspace transfer recovery for uncertain uploads.

Use `completed` and `failed` for Commission jobs. Reserve `replied`, `no_reply` and `--reply-id` for PM jobs. Set `--verified` only after obtaining concrete evidence. Reconciliation changes the local journal; determine Order delivery and completion from the service.

## Execute and Verify

1. Before `retry` or `reconcile`, stop the exact owning watch and acquire its account exclusively; both commands require the watch lock. Inspect the existing OS task and preserve its enabled state, command and environment. Use the platform's supported task control and await process exit. Identify foreground watch by its verified PID. Preserve unrelated processes.
2. Perform the selected journal recovery once. Restore a previously enabled watch using its verified existing task; use `watch --dispatch` in the bound environment for an explicitly authorized foreground run. Keep intentionally disabled tasks disabled.
3. Read `watch status` and `watch doctor` again; require the intended account, binding, `commission_order` subscription and execution-enabled ready runtime. Read `order get` and the matching job again to verify actual progress. Treat `pending` or `intake_recorded` as queued recovery.
4. Reuse verified artifacts and CLI checkpoints for remaining upload/delivery work. Preserve Order identity, sealed inputs and contract. Apply existing upload and idempotency recovery rules to uncertain submissions.
5. Stop after one selected recovery attempt if the same cause persists, evidence remains uncertain, the CLI rejects recovery or a retry limit is reached. Report the exact blocker and required next action. Notify once per unchanged cause and target; notify again on recovery, a new failure or required user action.

## Business Waits

- `awaiting_seller` with `input_request.status=waiting_buyer`: wait for formal buyer submission while required facts remain missing. When the seller confirms the existing materials are sufficient under the frozen contract, withdraw the obsolete request through `order withdraw-input-request` using the current Order version and request ID, then verify fresh background inspection and acceptance. Preserve the buyer's original materials; derive the writing scope from them and the fulfillment skill. Submitted buyer supplements also wake seller reinspection. Passing inspection permits acceptance, then payment is required.
- `pending_payment`: wait for verified payment. `in_progress` permits paid local fulfillment after ready input inspection.
- `validating`: wait for platform validation. `awaiting_buyer_confirmation`: follow buyer artifact inspection and completion approval.
- Report the observed Order, local recovery action, verified progress and remaining actor/action. Separate runtime readiness, job-stage completion, server delivery and buyer acceptance. Retain unverified status until the corresponding evidence exists.
