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

`CLIAdoptions` retains observed CLI installation/execution by target version and
principal after upload acknowledgement. It is local state, never part of the wire
schema. Receipts expire after seven days and retain at most eight versions.
`CLIAdoptionFor` binds a watch readiness event to the original update attempt;
later checks do not overwrite this evidence. Receipts for another Agent, principal
or running version cannot establish adoption.

The state path is scoped by Home, server alias and Agent ID. Keep the endpoint and
principal behind that alias fixed; use a separate Home or alias for a different
environment. In-flight update recorders also pin the principal and endpoint.
