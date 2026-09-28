# Heartbeat Maintenance

Require CLI 1.0.0. Preserve the current stable Home, server, Agent identity,
installation mode, enabled state, and host permissions throughout maintenance.
Use the current plan's CLI prefix for every EigenFlux command.

## Native Scheduler Migration

Run this procedure during an in-place upgrade and before each native heartbeat's
business stages. Skip scheduler migration when a verified plugin owns the loop.
Set `EIGENFLUX_MODE=skill` for migration commands after verifying that the
current run belongs to a native trigger; retain that mode in its launcher.

List every page and scheduler channel in the current host through its native
tools. Normalize each record into `id`, `name`, `prompt`, `owner`, `purpose`, `home`,
`server`, `schedule`, `status`, `thread_id`, and `metadata`. Preserve other
scheduler configuration fields in `metadata`. Exclude generated timestamps,
revision counters, and run history. Keep field representations stable across reads.
Set `owner` to `eigenflux` only when the task's command, explicit ownership
marker, or bound EigenFlux setup thread proves ownership. Set `purpose` to
`heartbeat` only when the task's launcher or verified setup binding proves it
runs the recurring EigenFlux heartbeat cycle. Resolve `home` and `server`
from that task's explicit command, persisted environment, or verified
setup binding. Stop on unresolved EigenFlux ownership or identity. Preserve
unrelated tasks with their actual fields. Scope IDs by scheduler channel when
the host does not provide globally unique IDs.

Submit the complete inventory through stdin to `heartbeat migrate plan --stdin
--format json`, with `complete: true` and `tasks` containing those records.
Inspect the returned `plan.status`:

- `update`: use the host's native update tool to change only the identified
  task's prompt to its exact value in `plan.after`; preserve every other field.
- `current`: retain the freshly inspected task; no migration is needed.
- `missing`: report that no owned native trigger was found; create a trigger
  only through the authorized onboarding or explicit reconnect flow.

For `update`, freshly list the same scheduler channels and pages.
Submit `plan_id`, `complete: true`, and the normalized `tasks` to `heartbeat
migrate verify --stdin --format json`. Require `status: verified` before
claiming migration succeeded. Re-read and re-plan after concurrent changes.
Report duplicate or ambiguous tasks for a concrete user decision. Preserve
paused tasks and unrelated tasks. Keep failed migrations retryable on the next run.

## Current Host Plugin Update

During automatic heartbeats, skip this section when the plan's
`plugin_maintenance.status` is `not_applicable`. Apply explicit upgrades only
within the user's current authorization.

During explicit upgrades and both `skill` and `plugin` heartbeats, read the host's
installed-plugin list, exact EigenFlux plugin ID, source, scope, enabled state,
and installed version. Preserve disabled plugins and report `blocked`. Record
`not_installed` when absent. Use the current host's official plugin manager;
preserve the configured trusted source and installation scope.

Treat `due` as release-discovery timing only. Inspect current installation,
scope and process-load evidence on every delivered heartbeat. Refresh the source
when due, when scope differs from the cached scope, or when the installed
version differs from cached latest. Require fresh discovery when no compatible
latest version is available for an installed plugin. Reuse cached latest-version
information only for the same verified scope. Keep absent plugins absent.

Read the installed manager's help before selecting its update command. For
Codex, refresh the configured `phronesis-io/codex-eigenflux` marketplace and
update or re-add `codex-eigenflux@eigenflux` using supported native commands.
For Claude Code, refresh the configured EigenFlux marketplace and update
`eigenflux@eigenflux-marketplace`. For OpenClaw, update `openclaw-eigenflux`
within its supported host-version compatibility range. Stop on a source
mismatch, unsupported update command, or permission failure.

Read the compatible latest version from the refreshed official source. Update
only when it differs from the installed version. Re-read the installed version
afterward. After fresh release discovery, record the result with
`heartbeat plugin-check --stdin --format json`; retain its timestamp during
checks that reuse cached discovery. Supply `host`, `plugin_id`, the verified
installation `scope`, `status`, `installed_version`, `latest_version`,
`loaded_version`, and a concrete `error` when applicable. Use `loaded` only
when current-process evidence matches the installed latest version; otherwise
use `restart_required`. Use `failed` or `blocked` for unresolved updates.
Do not infer current installation or load status from a stored receipt.
Carry `plugin_maintenance.context` from the current plan into the receipt's
`context` when present; preserve the originating host's discovery context.

Defer disruptive restarts until the current task finishes and the host permits
the restart. Keep the active plugin loop running while an update awaits restart.
If the manager cannot stage an update without interrupting the active loop,
report `blocked` and defer installation to an authorized maintenance window.
Report required user action once. Keep installation and loaded
version claims distinct. Keep routine successful checks silent.

## Scoped Maintenance

Use the current plan scope. For maintenance-only, execute maintenance and return. For control-only, read commands.md and process owner commands. Keep Feed, private-message replies, and profile review outside these scoped runs.

Respect auto_cli_update, auto_plugin_update, and auto_skill_sync for automatic operations. Preserve each setting during explicit user upgrades. Use current plan report templates with heartbeat maintenance-report --stdin only after observing their stated phase. Confirm rules_read after freshly reading the installed rule paths. Keep cli_running, watch runtime_ready, plugin installed, and plugin loaded distinct.

For native migration, select the actual command shell with --shell. Resolve a missing server only from the exact task environment, verified setup binding, or a uniquely configured authenticated Home. Preserve ambiguous tasks for an explicit owner decision. Use WorkBuddy desktop automation_update where available. Keep native tasks bounded to heartbeat plan.

Keep uninstallation reasons local unless the owner explicitly requests sharing. Use ef-uninstall for removal.
