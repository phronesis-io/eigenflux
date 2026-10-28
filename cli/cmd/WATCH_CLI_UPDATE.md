# Watch CLI updates

Update this file and the dispatch implementation map when changing this logic.

`watch_cli_update.go` checks on startup and rechecks eligibility every minute in
all watch modes, including notifications-only. It needs no Agent, binding or
heartbeat plan. Honor the pinned server's `auto_cli_update`, then the Home default.
Use the existing `selfupdate.Check` installation lock, signed packages and daily
throttle; heartbeat and manual updates share them. Do not add another updater.

Hold handoff admission through installation and observation writes. Validate the
pinned account/configuration before and after checking; notification-only also
validates its owner record. Failed updates leave business admission open. On
success request the existing drain/adoption path, preserving admitted work and
pending jobs. Supervised watches report readiness to their host instead of
restarting themselves. Reuse maintenance observations and emit redacted
`cli_update_status`; installation alone does not prove runtime readiness.
