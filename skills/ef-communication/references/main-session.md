# Main Session Binding

## ACP hosts

Require CLI 1.0.2. Bind Claude Code, OpenClaw, Hermes, WorkBuddy and other ACP
hosts only to an explicitly selected existing EigenFlux owner session and its
absolute work directory. Configure the local ACP host bridge as `main_command`;
preserve the ordinary execution command, arguments and mode.
Read its exact host session ID through the installed host; preserve account,
Home, server and binding revision. Require `watch doctor --probe-main` to report
`main_notice_owner_ready` before claiming connectivity. Report every non-Codex
host as not yet verified on the real host. Missing hidden-input capability leaves
the notice unavailable. Keep WorkBuddy local; use no cloud entry.

For an existing binding, select `--main-session-id` with `--expected-id` and
`--expected-revision`. Apply the stop, commit, readback and failure-cleanup
procedure below. Preserve established targets and pending work. Keep Codex
current-thread and scheduler selectors exclusive to native Codex bindings.

## Codex setup

Require CLI 1.0.2. Keep account identity, Agent Home, server and host-owned
conversation storage fixed. Complete initial binding automatically during
setup, an owner-requested upgrade, or a full native Codex heartbeat listing
`main_session_bootstrap`. Reuse the existing scheduler maintenance inventory
procedure. Treat initial binding as part of the existing upgrade authorization;
complete it before reporting the upgrade finished. Keep valid bindings unchanged.
During watch-owned private-message, maintenance or control execution, leave
binding pending for the independent host cycle; preserve the owning watch.

## Select and preflight

Read the host’s complete native scheduler inventory. Normalize ownership,
Home, server and `thread_id` using
`ef-broadcast/references/maintenance.md`. Include all channels and pages.
Require one active owned EigenFlux heartbeat for this account. For first-time
setup, persist and verify that heartbeat in the setup conversation before
binding. For existing installations, retain its registered target conversation.
Use the established task target even when the upgrade runs in another conversation.
Ask the owner only when verified target evidence is missing, paused or ambiguous.

Read the current binding and verified watch lifecycle entry. Record whether
watch is running and retain its account, command and original routing fields.
Keep inactive or disabled entries inactive. Keep the original watch running
through preflight.

Use `watch bind --preflight` with `--scheduler-stdin` or `--scheduler-file`.
Supply complete inventory through the selected input. Use file or stdin for
large inventories on every platform. For a missing watch binding, include the
new `--config`; for an existing empty main-session binding, omit `--config`
and supply its current `--expected-id` and `--expected-revision`.
Pin the actual Codex store with `--codex-home` when necessary. Require
`status: main_session_preflight` and retain `task_id`, `main_session_id`,
`expected_id` and `expected_revision`. Preserve the running watch on any
preflight failure. Resolve missing, paused, duplicate or ambiguous targets
with the owner. An explicit owner-selected `main_session_id` in the initial
configuration remains available when native scheduler evidence is unavailable.

## Commit and recover

Stop the previously running watch through its verified lifecycle entry.
Freshly read the same native scheduler channels after stopping it. Submit
that fresh complete inventory through file or stdin; pass the retained
`--expected-task-id`, `--expected-thread-id`, `--expected-id` and
`--expected-revision`. Keep `--config` only for creation. Omit `--preflight`.
Require the unchanged unique task and target. Read back the resulting binding.

After stopping watch, always complete lifecycle cleanup before reporting.
On a failed commit, verify that account and original routing fields remain
unchanged; restart the same previously running entry with its original command
and require fresh runtime readiness. On an uncertain command result, read the
binding: treat the exact intended route as committed, the unchanged original
route as failed, and any other route or identity as requiring owner action.
Preserve pending jobs and send receipts. Report cleanup failure separately;
keep stopped, disabled or identity-changed entries from automatic activation.
Leave an unsuccessful initial binding retryable in the next full native heartbeat.

On a confirmed commit, run `watch recover` with the fixed Home and server
when watch was previously running or the current setup authorizes startup.
Require `mode: dispatch`, `status: ready`, fresh `runtime_ready` and a successful
main-conversation probe. Preserve the account service and let recovery replace
its notification-only command with dispatch. Keep explicitly stopped or disabled
entries inactive. Report incomplete recovery and retry through the next full
host cycle. Preserve the heartbeat target; later target changes require an
explicit owner-directed switch.

## Explicit switching

Require the owner’s instruction to select the new main conversation. Preflight
`watch bind --current-session` in that conversation, or `--main-session-id`
with its host-verified exact ID. Supply the old ID and revision through
`--expected-id` and `--expected-revision`. Apply the same stop, commit,
readback and failure-cleanup procedure. Keep the established binding until
switching succeeds. Treat `unavailable` as requiring owner action.

Automatic successor discovery remains unsupported. Execute initial binding in
the independent host upgrade or heartbeat cycle; serialize explicit switching
with the desktop and heartbeat.
Claim PM acceptance only after matching requested/returned session IDs,
decision and send receipt in logs.
