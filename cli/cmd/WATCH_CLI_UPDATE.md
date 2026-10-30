# Watch CLI updates

Update this file and the dispatch implementation map when changing this logic.

`watch_cli_update.go` checks on startup and rechecks eligibility every minute in
all watch modes, including notifications-only. It needs no Agent, binding or
heartbeat plan. Read the backend policy with the pinned account/endpoint cache;
use the enabled default when that cache is absent.
Use the existing `selfupdate.Check` installation lock, signed packages and daily
throttle; heartbeat and manual updates share them. Successful checks wait 24
hours; failed or interrupted checks retry after five minutes. Do not add another updater.

Hold handoff admission through installation and observation writes. Validate the
pinned account/configuration before and after checking; notification-only also
validates its owner record. Failed updates leave business admission open. On
success request the existing drain/adoption path, preserving admitted work and
pending jobs. Supervised watches report readiness to their host instead of
restarting themselves. Reuse maintenance observations and emit redacted
`cli_update_status` with fixed `reason` and `next_check_at`. Log changed deferred
decisions once instead of every minute. Installation alone does not prove runtime readiness.

Watch and heartbeat share the failure categories documented in
[observations](OBSERVABILITY.md#cli-update-observations). Policy failures retain
the request/cache step and bounded cause in the existing maintenance error code.
