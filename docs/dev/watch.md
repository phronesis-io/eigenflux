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

`restart_required` asks the existing plugin supervisor to stop its old watch,
confirm exit, and start the same arguments using a clean environment. The
supervisor preserves active Agent turns, checks the pinned account, and waits
for a new `runtime_ready`. Binary replacement or `version --short` alone does not
prove runtime adoption. Sharing a binary shares installed versions; per-Home
auto-update switches control initiation, not independent version pinning.

Maintenance reporting and Skills synchronization are shared with native
heartbeats. Native scheduled tasks remain finite `heartbeat plan` calls. Their
migration changes only the prompt through host tools, preserves pause/cadence
and binding, and verifies fresh readback. Legacy missing server fields require
a verified unique binding. POSIX, PowerShell and CMD launchers use distinct
quoting; CMD rejects expansion characters that cannot be represented safely.

The existing PM server marks messages read before delivery. This version does
not promise replay across that failure window. Windows locked executable
replacement stays blocked rather than forcing host termination. Long control
task renewal, reliable PM history replay, Codex event runners and external
WorkBuddy desktop invocation are separate extensions.

Fatal `identity_changed`, `configuration_changed`, and `owner_replaced`
diagnostics stop the account background runtime. Exit code 78 is authoritative
when the output pipe cannot deliver the final diagnostic. Network failures retain
bounded reconnection. Credential lock waits and refresh requests are cancellable.
Runtime adoption is recorded only after PM and Runtime readiness; a separate,
bounded worker retries queued maintenance observations without blocking leases.

TODO: route future Agent-to-Agent delegation availability through the same
account-scoped watch after its authorization, claim/cancel and result protocol
is specified. No delegation capability or A2A handler is registered now.
