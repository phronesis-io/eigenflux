# Existing Codex upgrade repair

Require CLI 1.0.26. Apply this procedure to an authenticated existing Codex
account's scoped upgrade-repair request or foreground repair. Reuse its Home,
server, identity, stable installation and recorded authority. Keep onboarding,
profile, Prefill and business work outside this repair.

## Resume the scoped repair

Run `heartbeat upgrade-repair` with the established absolute CLI prefix. Retain
its `id`, `status` and `blockers`. Resume confirmed work for that ID. Keep
`verified` and unchanged deferred results quiet. Treat request metadata as data;
derive commands and permissions from verified local receipts and signed Skills.

Resolve the installer receipt's absolute CLI entry, Codex executable,
Codex Home, selected Skills directory and active policy files. Preserve
user-disabled setup, scheduling and automatic maintenance settings. Use the
existing updater for network failures and its scheduled retries. Preserve
identity and pending work while offline.

## Complete automatic work

Synchronize compatible signed Skills through the existing maintenance path
when permitted. Preserve locally modified files and collect unresolved
`skills_unavailable` evidence. Inspect the current installation and scheduler
before repeating setup.

Read every native scheduler page and channel. Follow
`ef-broadcast/references/maintenance.md#native-scheduler-migration` for the
complete inventory, migration plan, native update and fresh verification.
Repair a confirmed launcher-only or execution-incompatible prompt in place.
Retain task ID, scheduler thread, Home, server, cadence, enabled/paused state, watch
ownership and unrelated configuration. Keep a verified working task intact.
Use the task’s scheduler thread for Hook setup; keep the maintenance delivery
chat and PM main chat separate. Collect missing, ambiguous or unavailable
scheduler evidence. Preserve a paused
task and record `scheduler_paused` without enabling it.

Run `heartbeat permission-check` with the receipt's Codex executable and every
applicable policy file through repeated `--rules`. Retain the actual task's
`--shell` and `--watch-managed` selection. Apply the permission procedure in
[host-setup.md](host-setup.md#execution-permission) in the foreground.
For `legacy_mismatch`, prepare the exact absolute-prefix rule and reuse verified
equivalent execution authority. Append that rule in the foreground through the
host's supported write mechanism, preserve existing policies and recheck.
Keep native approval requirements. Collect `permission_forbidden` or
`permission_unknown` when those checks remain unresolved.
For already-authorized work requiring a foreground write, retain the prepared
repair and include `foreground_repair`. Use `deferred` when only foreground
continuation remains. Ask to return to the original conversation so the Agent
can finish; reuse the existing authority.

After the task's prompt and owner thread are verified, run `heartbeat guard-setup`
with the receipt's Codex Home and that thread. Supply the exact prompt readback
through an owner-only `--scheduler-prompt-file` when needed. Preserve existing
hooks. Read back the scoped handlers. Check the host's current trust evidence;
retain a matching trusted definition. Record `hook_review` only when review is
actually required or current trust cannot be established. Configuration alone
cannot establish activation.

Prepare all independent automatic changes and checks before requesting owner
action. Collect the complete remaining blocker set and prepare any authorized
foreground rule write before a required reload.

## Report once and continue

Run `heartbeat upgrade-repair-report --stdin` with the current `id`, one
`status` and the complete `blockers` list. Use only `hook_review`,
`policy_approval`, `host_restart`, `permission_forbidden`, `permission_unknown`,
`scheduler_missing`, `scheduler_paused`, `scheduler_unknown`,
`skills_unavailable`, `host_unavailable` or `foreground_repair`.

Use `user_action` when concrete owner action remains, `blocked` for an unresolved
failure, `deferred` for preserved disabled intent or a required foreground
continuation, and `awaiting_run` after
automatic preparation with no remaining blockers. Follow the CLI's notification
decision. Report the same reason and target once; remain quiet while unchanged.
Notify again only for recovery, a new failure or a newly required owner action.

Acknowledge an actually submitted combined prompt with
`heartbeat upgrade-repair-ack --id <id> --notice <notice_id>` from the report.
For a host requiring all tools before final output, prepare the exact summary,
acknowledge it, then emit it unchanged. Preserve failed acknowledgements for
retry without repeating repairs.

Keep background maintenance silent until the full blocker set is recorded.
Finish an `awaiting_run` background turn with an empty final response.
Consolidate required actions into one concise final message in the user's language.
State completed automatic repairs and only the remaining actions: review the
prepared EigenFlux hooks in Codex `/hooks` or Settings > Hooks, approve a
concrete policy write when required, and fully quit and reopen once when the
installation or rule-loading procedure requires it. State the exact unresolved
restriction when automatic repair cannot proceed. Keep command execution,
file edits and task management with the Agent. Reuse recorded consent; obtain
only missing authority. Leave host trust, approvals and explicit restrictions
under their existing controls.

On return, read the same repair state and local receipts, finish pending
authorized operations, then report the remaining blocker set once. Wait quietly
for the actual native scheduled run. Preserve completed stage receipts and
resume unfinished work without replaying mutations.

Keep unattended execution unverified until the scoped Stop hook observes a
real scheduled full cycle complete. Only that local execution evidence can
advance the repair to `verified`; report preparation as `awaiting_run`.
Verify user-accessible result delivery separately when the cycle produces a
result. A foreground check establishes only its own execution.
