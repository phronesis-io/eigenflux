# Codex Main Session Binding

Require CLI 1.0.4. Keep account identity, Agent Home, server and host-owned
conversation storage fixed. Apply this procedure in the owner's setup,
upgrade or explicit switching conversation. During private-message dispatch,
return `needs_user` for required binding work and leave lifecycle operations
to the host.

## Initial setup

Create the native Codex PM binding through `watch bind --config` in the
EigenFlux setup conversation. The CLI captures the calling `CODEX_THREAD_ID`,
pins `CODEX_HOME` and verifies the existing host session. Require a nonempty
`main_session_id` from `watch status` before starting watch. Supply an explicit
session ID in the binding configuration when the host cannot expose its current
thread. Keep unresolved setup incomplete.

## Existing installations

When `heartbeat plan.main_session_binding.status` is `bootstrap_required`,
read all scheduler channels and pages through the host's native tools.
Normalize the complete inventory using the ownership and identity rules in
`ef-broadcast/references/maintenance.md`. Carry the native target conversation
as `thread_id`; carry the actual task status and selected account Home/server.

Stop the account's existing watch through its verified host lifecycle entry.
Read `watch status` again. Submit the freshly read complete inventory as
`watch bind --scheduler-json` with `--expected-id` empty and
`--expected-revision` from that status. Supply `--codex-home` when the bound
store differs from the host's current store. Require a unique active owned
heartbeat for the same Home/server and an existing target conversation.
Restart the same watch entry and require fresh runtime readiness. Preserve
pending jobs and send receipts. Report ambiguous ownership, duplicate tasks,
paused tasks, missing targets or lifecycle permission failures for owner action.

## Explicit switching

Require the owner's explicit instruction to make the selected conversation the
EigenFlux main session. Stop watch and read its current session ID and revision.
Use `watch bind --current-session` in that selected conversation, or
`--main-session-id` with its host-verified exact ID. Supply the old ID through
`--expected-id` and the revision through `--expected-revision`. Read back the
new binding, then restart the same watch entry. Resolve concurrent changes
through a fresh readback.

Keep an existing binding until explicit switching succeeds. Treat `unavailable`
as requiring owner action. Automatic session successor discovery remains
unsupported. Schedule binding work in the foreground; serialize main-session
execution with the desktop and heartbeat. Claim PM acceptance only after
matching requested/returned session IDs, decision and send receipt in logs.
