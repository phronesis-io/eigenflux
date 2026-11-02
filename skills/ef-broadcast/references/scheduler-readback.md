# Native Scheduler Readback

Keep the established Agent Home, server, task identity, cadence and enabled state.
Check `heartbeat scheduler-report --help` with the current CLI prefix. Continue
the established procedure when the command is unavailable; retain unknown
receipt coverage for that CLI.

After Agent authentication, read every native scheduler channel and page afresh.
Normalize records into `id`, `name`, `prompt`, `owner`, `purpose`, `home`, `server`,
`schedule`, `status`, `thread_id` and `metadata`. Derive ownership and identity
from the task's command, persisted environment or verified setup binding.
Preserve native enabled, paused, disabled and unrecognized states. Capture
`observed_at` as the UTC Unix millisecond time of this read.

Submit `observed_at`, `complete: true` and the normalized `tasks` through stdin
to `heartbeat scheduler-report --stdin --format json` within five minutes.
Require `status: recorded` and inspect `scheduler_state`. Preserve missing,
paused, disabled and unknown states in the setup result. Retain `queued`
observations for later upload.

On native API failure, submit `observed_at` and the matching bounded
`error_code`: `scheduler_api_unavailable`, `scheduler_readback_denied` or
`scheduler_readback_failed`. Keep the failure explicit and preserve tasks.
Keep task verification separate from runtime readiness and completed Agent work.
