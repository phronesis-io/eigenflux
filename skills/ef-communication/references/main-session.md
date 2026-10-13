# Codex Main Session Binding

Require CLI 1.0.5. Keep account identity, Agent Home, server and host-owned
conversation storage fixed. Apply this procedure in the owner’s setup,
upgrade or explicit switching conversation. During private-message dispatch,
return `needs_user` for binding work and leave lifecycle operations to the host.

## Select and preflight

Read the host’s complete native scheduler inventory. Normalize ownership,
Home, server and `thread_id` using
`ef-broadcast/references/maintenance.md`. Include all channels and pages.
Require one active owned EigenFlux heartbeat for this account. For first-time
setup, persist and verify that heartbeat in the setup conversation before
binding. For existing installations, retain its registered target conversation.

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

On a confirmed commit, restart the same previously running entry and require
fresh runtime readiness. Keep an initially inactive entry inactive until the
owner’s setup flow starts it. Preserve the heartbeat target; later target changes
require an explicit owner-directed switch.

## Explicit switching

Require the owner’s instruction to select the new main conversation. Preflight
`watch bind --current-session` in that conversation, or `--main-session-id`
with its host-verified exact ID. Supply the old ID and revision through
`--expected-id` and `--expected-revision`. Apply the same stop, commit,
readback and failure-cleanup procedure. Keep the established binding until
switching succeeds. Treat `unavailable` as requiring owner action.

Automatic successor discovery remains unsupported. Perform binding work in
the foreground and serialize execution with the desktop and heartbeat.
Claim PM acceptance only after matching requested/returned session IDs,
decision and send receipt in logs.
