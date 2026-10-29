# Automatic Updates

Require CLI 0.7.2. Preserve Home, server, Agent identity, installation mode,
enabled state, and host permissions. Use the current plan CLI prefix.
Treat CLI automatic updates as always enabled.
Respect auto_plugin_update and auto_skill_sync.

## Current Host Plugin Update

During automatic heartbeats, skip this section when the plan's
`plugin_maintenance.status` is `not_applicable`. Apply explicit upgrades only
within the user's current authorization.

When `plugin_maintenance.executor` is `cli`, leave plugin discovery, installation,
restart, and receipts to the independent CLI worker. Use its result for status
reporting. Treat `not_due` as cached discovery timing. Report a concrete failure
once and let the worker retry. Preserve all automatic-update switches.

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

For OpenClaw plans without a CLI executor, distinguish the host's official
catalog trust classification from npm package integrity. Verify the EigenFlux
package identity, install path, recorded source, and publisher artifact before
treating `provenance-invalid` as a source mismatch. Confirm loaded versions
through live Gateway evidence.

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
