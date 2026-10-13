# Account watch and CLI adoption

`eigenflux --homedir <stable-home> --server <server> watch` runs one account's
PM, control and maintenance notification transports. One canonical Home/server
OS lock prevents concurrent watch owners. Account, principal or endpoint changes
stop the watch; renewed credentials for the same identity continue normally.
The existing `stream` command remains available.

Output is JSON lines with schema `eigenflux_watch.v1`, `type`, `event_id`,
`state_scope` and `data`. Scope pins Home, server, Agent and principal. `started`
reports the running CLI version, commit and PID. `runtime_ready` additionally
requires a connected PM socket and a successful Runtime heartbeat.
`runtime_unavailable` invalidates a previously ready connection; recovery emits
a new `runtime_ready`. Diagnostics contain fixed codes rather than tokens.

Control SSE wakes the existing Runtime heartbeat reconciliation. Watch emits
`control_pending` but never claims or executes commands. The host uses
`heartbeat plan --control-only`. Profile and maintenance hints reuse the existing
bounded heartbeat plan; receiving a hint is not a completion receipt.

Every minute, watch checks identity, configuration and the installed executable.
A newer valid CLI closes PM HTTP pull admission. A pull already in progress
finishes caching and writing its `pm_push` output before adoption; no further
page starts. HTTP fallback runs once per minute with at most 32 single-message
pages. Caching pins the original server and shares the credential lock with
account changes. PM event output includes locally assigned `transport` and
`received_at`; these describe reception, not successful Agent execution.

Standalone watch drains admitted pulls, releases its account lock and adopts
the installed executable with the original arguments, canonical Home, pinned
server and inherited environment. POSIX replaces the process in place. Windows
starts a replacement with a signal-forwarding parent. The replacement validates
the expected Agent and principal before refreshing credentials.

Host plugins set `EIGENFLUX_WATCH_SUPERVISED=1`. Once drained, watch emits
`restart_required` with `ready: true` and waits for the host to stop it. The host
must respect this drain signal, confirm the old watch's exit and await the new
watch's `runtime_ready`. Leases and socket reception remain active during the
host handoff. A version probe or process start alone does not prove adoption.

Fatal identity/configuration/owner-replacement errors return exit code 78.
Network errors reconnect with bounded backoff. Credential refresh and lock waits
respect cancellation and never create a replacement private identity.

The PM API marks messages read before network delivery. The existing message
cache is best-effort; network failure, process kill or cache write failure does
not provide reliable replay. Update draining protects normal HTTP pulls, without
adding delivery ACKs or an execution journal. This watch does not dispatch Agent
work or run Commission/desktop notification workers. Windows locked executable
installation remains blocked without forcibly terminating running processes.

Regression checks from `cli/`:

```sh
go test -race ./cmd ./internal/auth ./internal/watch -run 'TestWatch|TestPinned|TestV2Credential'
```
