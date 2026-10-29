# Account maintenance observations

Update this map whenever queue or event logic changes.

`events.go` owns validation, account-scoped atomic storage, bounded retention,
deduplication, backoff and acknowledgements. Local events retain at most 256
entries for seven days. Network work happens outside the state lock; only
acknowledged event IDs are removed.

`Flush` sends the oldest fifty observations and honors automatic backoff.
`FlushUninstall` selects only the current uninstall attempt and bypasses that
backoff, so an online removal is observable before its CLI exits even with an
old maintenance backlog. It preserves unrelated history. The caller pins account
identity, supplies a bounded HTTP budget, and carries no free-text reason into
events. See `cli/cmd/UNINSTALL.md` for the uninstall lifecycle and server contract.

Heartbeat CLI results include `reason` for skipped, locked, deferred and failed
checks; `next_check_at` identifies the existing 24-hour throttle deadline. These
non-attempts do not create failure observations. CLI phase failures and Skills
plan failures emit a local `[eigenflux:maintenance]` line with `attempt_id`,
`phase` and a fixed `error_code`. Observations contain fixed classifications,
never raw error text. Local Skills validation reports the current CLI, required
CLI and manifest revision without relaxing signature or rollback checks.

The heartbeat verifies the local manifest signature even when automatic sync
retains an existing installation. Compatible, correctly signed offline Skills
remain usable without creating verified-remote compatibility evidence.
