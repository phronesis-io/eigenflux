# Uninstall implementation

Update this file whenever the uninstall contract changes.

- `uninstall.go`: installation ownership, locks, preview, wrapper registration,
  owned Skills/scripts, local data cleanup, optional shared notification removal, binary removal.
- `uninstall_events.go`: account-pinned observations, receipts, local audit,
  bounded upload, Windows original-file verification and finalization.
- `uninstall_state.go`: atomic local JSON persistence shared with heartbeat maintenance.
- `uninstall_scope.go`: current Home selection and shared resource ownership.
- `uninstall_purge.go`: validated Home business-data cleanup, retaining live locks.
- `internal/uninstallscript`: embedded wrappers; keep byte-identical to `static/`.
- `internal/desktopnotify/uninstall_*`: owned per-user notification cleanup.

Versioned CLI children resolve the validated stable installation entry for
registration and uninstall. Installer source copies and temporary Windows
reporters keep the actual running executable path. Native heartbeat launchers,
CLI prefixes and newly created watch services/bindings also use the stable entry.
Existing owned version directories and activation pointers remain preserved by
this uninstall implementation; their verified cross-platform cleanup needs a
separate receipt contract. Never recursively remove an unverified versions tree.

Registration writes `<executable>.uninstall.sh` or `.uninstall.ps1` and stores
its SHA-256 in the installation record. Registered scripts are removed even when edited.
Preview is immutable. `--begin` records intent without removal; the Skill carries
its `--attempt` through native host cleanup and final apply. `--record-result`
records blocked/failed native cleanup. Apply still requires native cleanup
confirmation, explicit current/all scope, and stopped watchers in that scope.
The Skill asks whether other Agents still need EigenFlux before cleanup. Current
scope keeps the CLI, other Homes, shared Skills and notification integration,
then removes only current Home registrations. All scope removes the installation.
Current removal preserves Skills outside the current Home even when no other
Agent is registered. A missing Skills directory is already removed; an existing
directory still requires its ownership manifest.
Scope is pinned in the begin receipt; changing it requires a fresh attempt.

Official installers use the downloaded CLI's internal `installation replace`
command to replace the binary and register ownership under install/update locks.
Windows installs managed Skills through sync and records the actual target.

The event is `maintenance_attempt`, `phase=uninstall`, `trigger=manual`.
Results: `started`, `removed`, `partial`, `failed`, `blocked`. Source distinguishes
`agent` and `manual`. Platform, version, duration, and fixed error code are
recorded; free-text reasons and paths never enter telemetry. Categories are
`not_provided`, `other`, `usability`, `cost`, `performance`, `privacy`,
`no_longer_needed`. One physical attempt may have multiple account observations;
count distinct attempt IDs, not rows. Terminal result currently summarizes the
CLI-led removal; native host cleanup failure is reported by the Skill.

Started observations use local audits and queues. Terminal observations capture
existing authenticated clients in memory, clear local data, and upload the actual
cleanup result without recreating local business files. Upload uses existing tokens with no
refresh/login and one five-second network budget across accounts. The current
uninstall attempt bypasses unrelated queue history and automatic backoff. No authenticated
identity means local audit only. Offline removal cannot guarantee later delivery.
The existing maintenance queue retains events for seven days; the server retains
accepted events for thirty days. Started-only attempts have unknown outcomes.
Temporary account selection uses `config.UseHomeDir` to restore the original
flag/environment/default resolution after each observation or upload pass.

On Windows the script copies the current CLI into a temporary directory after
the original exits. Only that copied reporter drops the three internal
`EIGENFLUX_INSTALL_*` routing markers; the original entry keeps its installation
context. Finalization reacquires install/update/watch locks, verifies
receipt-owned file hashes, removes unchanged originals, observes absence, records
the result, and cleans the copy. An intervening update or registration blocks
deletion. Cleanup failures retain failure state for inspection. Abnormal wrappers
still allow Home cleanup, report partial and exit unsuccessfully. The Windows
wrapper prints the actual cleanup state even when the finalizer reports partial.
Successful Home cleanup clears receipts without recreating them. Receipts carry no credentials or reason text.

Notification cleanup is explicit because all accounts share one integration.
macOS requires the installed bundle ownership digest and stops the helper through
its existing quit protocol. Windows verifies the dedicated shortcut, AUMID and
icon. Unknown/edited assets stay intact. OS permission records and shared PATH
entries are retained. Cloud identities remain. Successful cleanup deletes local credentials, identity
keys, configuration, cached history, audits, and edited managed Skills; only
shared lock inodes and unrelated host content remain. This change does not configure Grafana dashboards or alerts.
