# Heartbeat Maintenance

Require CLI 0.0.48. Preserve the current stable Home, server, Agent identity,
installation mode, enabled state, and host permissions throughout maintenance.
Use the current plan's CLI prefix for every EigenFlux command.

## Native Scheduler Migration

Run this procedure during an in-place upgrade and before each native heartbeat's
business stages. Skip scheduler migration when a verified plugin owns the loop.
Set `EIGENFLUX_MODE=skill` for migration commands after verifying that the
current run belongs to a native trigger; retain that mode in its launcher.

List every page and scheduler channel in the current host through its native
tools. Normalize each record into `id`, `name`, `prompt`, `owner`, `home`,
`server`, `schedule`, `status`, `thread_id`, and `metadata`. Preserve other
scheduler configuration fields in `metadata`. Exclude generated timestamps,
revision counters, and run history. Keep field representations stable across reads.
Set `owner` to `eigenflux` only when the task's command, explicit ownership
marker, or bound EigenFlux setup thread proves ownership. Resolve `home` and
`server` from that task's explicit command, persisted environment, or verified
setup binding. Stop on unresolved EigenFlux ownership or identity. Preserve
unrelated tasks with their actual fields. Scope IDs by scheduler channel when
the host does not provide globally unique IDs.

Submit the complete inventory through stdin to `heartbeat migrate plan --stdin
--format json`, with `complete: true` and `tasks` containing those records.
Inspect the returned `plan.status`:

- `update`: use the host's native update tool to change only the identified
  task's prompt to its exact value in `plan.after`; preserve every other field.
- `current`: retain the task and verify it with a fresh read.
- `missing`: report that no owned native trigger was found; create a trigger
  only through the authorized onboarding or explicit reconnect flow.

For `update` or `current`, freshly list the same scheduler channels and pages.
Submit `plan_id`, `complete: true`, and the normalized `tasks` to `heartbeat
migrate verify --stdin --format json`. Require `status: verified` before
claiming migration succeeded. Re-read and re-plan after concurrent changes.
Report duplicate or ambiguous tasks for a concrete user decision. Preserve
paused tasks and task count. Keep failed migrations retryable on the next run.

## Current Host Plugin Update

During explicit upgrades, check the current host plugin. During heartbeat,
check it only when the plan marks plugin maintenance due. Read the host's
installed-plugin list, exact EigenFlux plugin ID, source, scope, enabled state,
and installed version. Preserve disabled plugins and report `blocked`. Record
`not_installed` when absent. Use the current host's official plugin manager;
preserve the configured trusted source and installation scope.

Read the installed manager's help before selecting its update command. For
Codex, refresh the configured `phronesis-io/codex-eigenflux` marketplace and
update or re-add `codex-eigenflux@eigenflux` using supported native commands.
For Claude Code, refresh the configured EigenFlux marketplace and update
`eigenflux@eigenflux-marketplace`. For OpenClaw, update `openclaw-eigenflux`
within its supported host-version compatibility range. Stop on a source
mismatch, unsupported update command, or permission failure.

Read the compatible latest version from the refreshed official source. Update
only when it differs from the installed version. Re-read the installed version
afterward. Record the result with `heartbeat plugin-check --stdin --format json`.
Supply `host`, `plugin_id`, `status`, `installed_version`, `latest_version`,
`loaded_version`, and a concrete `error` when applicable. Use `loaded` only
when current-process evidence matches the installed latest version; otherwise
use `restart_required`. Use `failed` or `blocked` for unresolved updates.

Defer disruptive restarts until the current task finishes and the host permits
the restart. Report required user action once. Keep installation and loaded
version claims distinct. Keep routine successful checks silent.
