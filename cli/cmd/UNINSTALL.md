# Uninstall implementation

Update this file whenever the uninstall contract changes.

- `uninstall.go`: installation ownership, locks, preview, wrapper registration,
  unchanged Skills/scripts, optional shared notification removal, binary removal.
- `uninstall_events.go`: account-pinned observations, receipts, local audit,
  bounded upload, Windows original-file verification and finalization.
- `internal/uninstallscript`: embedded wrappers; keep byte-identical to `static/`.
- `internal/desktopnotify/uninstall_*`: owned per-user notification cleanup.

Registration writes `<executable>.uninstall.sh` or `.uninstall.ps1` and stores
its SHA-256 in the installation record. Changed scripts remain untouched.
Preview is immutable. `--begin` records intent without removal; the Skill carries
its `--attempt` through native host cleanup and final apply. `--record-result`
records blocked/failed native cleanup. Apply still requires native cleanup
confirmation, shared-installation scope, and stopped watchers.

The event is `maintenance_attempt`, `phase=uninstall`, `trigger=manual`.
Results: `started`, `removed`, `partial`, `failed`, `blocked`. Source distinguishes
`agent` and `manual`. Platform, version, duration, and fixed error code are
recorded; free-text reasons and paths never enter telemetry. Categories are
`not_provided`, `other`, `usability`, `cost`, `performance`, `privacy`,
`no_longer_needed`. One physical attempt may have multiple account observations;
count distinct attempt IDs, not rows. Terminal result currently summarizes the
CLI-led removal; native host cleanup failure is reported by the Skill.

Local audits and queues precede upload. Upload uses existing tokens with no
refresh/login and one five-second network budget across accounts. The current
uninstall attempt bypasses unrelated queue history and automatic backoff. No authenticated
identity means local audit only. Offline removal cannot guarantee later delivery.
The existing maintenance queue retains events for seven days; the server retains
accepted events for thirty days. Started-only attempts have unknown outcomes.
Temporary account selection uses `config.UseHomeDir` to restore the original
flag/environment/default resolution after each observation or upload pass.

On Windows the script copies the current CLI into a temporary directory after
the original exits. Finalization reacquires install/update/watch locks, verifies
receipt-owned file hashes, removes unchanged originals, observes absence, records
the result, and cleans the copy. An intervening update or registration blocks
deletion. Finalization retries return the stored terminal result without deleting
a subsequent installation. Receipts carry no credentials or reason text.

Notification cleanup is explicit because all accounts share one integration.
macOS requires the installed bundle ownership digest and stops the helper through
its existing quit protocol. Windows verifies the dedicated shortcut, AUMID and
icon. Unknown/edited assets stay intact. OS permission records and shared PATH
entries are retained. Cloud identities, credentials, history, and edited Skills
are preserved. This change does not configure Grafana dashboards or alerts.
