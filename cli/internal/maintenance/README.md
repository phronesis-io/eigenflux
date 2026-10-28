# Account maintenance observations

Update this map whenever queue or event logic changes.

`events.go` owns validation, account-scoped atomic storage, bounded retention,
deduplication, backoff and acknowledgements. Local events retain at most 1024
entries for seven days. Network work happens outside the state lock; only
acknowledged event IDs are removed.

`Flush` sends the oldest fifty observations and honors automatic backoff.
`FlushUninstall` selects only the current uninstall attempt and bypasses that
backoff, so an online removal is observable before its CLI exits even with an
old maintenance backlog. It preserves unrelated history. The caller pins account
identity, supplies a bounded HTTP budget, and carries no free-text reason into
events. See `cli/cmd/UNINSTALL.md` for the uninstall lifecycle and server contract.

Notification `status` observations may carry a complete `device_id` /
`device_name` pair. Device IDs are random 32-hex installation IDs; names are
nonempty sanitized strings of at most 64 Unicode characters. Setup and send
events cannot carry this pair. Old records without device metadata remain
valid. The existing `result`, `error_code`, `platform` and original `event_at`
describe permission observations; queue upload time must not refresh their age.

Commission diagnostics reuse this queue and its upload acknowledgements. Keep
[the stage contract](../../../docs/dev/commission-observability.md) synchronized.

Updater observations use the same queue with an identity-pinned token and endpoint.
Uploads do not refresh credentials or mutate business state. CLI update phase
observers remain best effort; rollback finishes before cancellation returns.
