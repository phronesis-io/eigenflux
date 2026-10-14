# Existing watch task restart

Update this map with every lifecycle change.

`cmd/watch_notifications.go` exposes Windows `watch start`. `start_windows.go`
and `start_windows.ps1` install an account-scoped, non-elevated interactive task
named `EigenFlux-Notifications-<scope>`. Launcher JSON contains pinned identity
and paths, never tokens. `notification_watch_windows.ps1` clears inherited
EigenFlux overrides and invokes `watch --notifications-only`, logging under
the explicit Home's `watch/` directory. Repeated starts reuse a running task;
disabled/foreign tasks fail without mutation. New tasks start immediately and
on user logon. Three scheduler failure retries retain the pinned account.
`process_started` still requires fresh `notifications_ready` in the log.

`cmd/watch_restart.go` exposes `watch restart`. `restart.go` requires exactly one
explicit target and bounds the operation to 45 seconds. The task definition
selects the account; `--homedir` and `--server` do not retarget it. No discovery,
new service, credential changes, binding changes or task-definition rewrites.
The capability registry marks this local operation as requiring CLI 1.0.3 and
an explicit user instruction.

- macOS: `--launch-agent` selects an owned regular plist in the current user's
  `~/Library/LaunchAgents`. Validate its executable, working directory, GUI
  session and enabled state before stopping anything. Reload an existing service
  with bootout/bootstrap; bootstrap an absent service. Kickstart without `-k`
  avoids killing a process already started by RunAtLoad. Require the same PID in
  running state for three seconds. Preserve the original wrapper and test-source.
- Windows: `--scheduled-task` selects one exact COM task path. Require the current
  user's SID, interactive logon, no elevation, and an enabled on-demand task.
  Stop existing instances, run the original definition, and require the new
  instance to remain running for three seconds. JSON stdin carries the path to
  the embedded script; never interpolate it into PowerShell source.
  Compare the returned COM instance by reference, then allow asynchronous
  startup before refreshing it; PowerShell value equality can query a not-yet
  available COM default property and falsely report `start_failed`.

Success is `process_started`, with `runtime_verified:false`; a wrapper PID or
scheduler instance does not prove watch connectivity or Agent execution. Check
fresh `runtime_ready` with matching `cli_commit`/`pid` and notification status separately. Startup/reload failure
returns nonzero and directs the caller to existing task logs; a failed reload
may leave the task stopped. Disabled tasks remain disabled.

Update the existing wrapper/task paths to the complete new test package before
restarting. `install-local.sh --watch-launch-agent <plist>` invokes this command
after installation, even if notification setup fails. Without an explicit target,
updates do not infer or restart a background task. Running restart from the task
being restarted is unsupported; use the foreground installer/operator session.

Tests simulate launchctl and use an in-memory Windows scheduler. Native
acceptance must check loaded, unloaded, immediate-exit and wrapper/source failure
cases on each OS. Never operate on real account tasks in automated tests.
