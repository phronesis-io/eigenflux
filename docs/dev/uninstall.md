# CLI installation removal

`eigenflux uninstall` previews the installation registered beside the running
executable. Preview performs no network requests and does not write files,
including when Skills inspection is requested. Apply and explicit `--begin`
record account-scoped uninstall observations. See the maintained implementation
map in `cli/cmd/UNINSTALL.md`.

The installers register the actual executable selected or installed, the
normalized Agent Home, and the invoking host. The POSIX installer also records
the successfully synchronized managed Skills target. The Windows direct-copy
installer preserves its unregistered Skills directory. The POSIX GitHub fallback and Windows direct-copy
installer leave the registered Skills target empty and report the preserved
provisional directory, even if a manifest from an earlier sync remains there.
After a successful `skills sync`, explicitly register its actual target to
include it in installation-wide removal.
`installation record --host HOST --skills-target PATH` also supports explicit
registration. An explicitly empty `--skills-target=` records no Skills target.
Registrations are deduplicated by Home and host; registrations from other Homes
remain in the shared executable's installation record.

Before applying removal, stop and remove the owning host's native scheduled
triggers and plugin consumers. The CLI does not manipulate scheduler databases
or uninstall host plugins. Apply requires `--host-cleanup-confirmed`; an
installation with multiple registrations additionally requires `--all-homes`.
These flags confirm the reviewed scope; they do not discover unregistered hosts.

Apply holds the installation, update, and registered Home watch locks. Retained
watch locks are checked even when their server is no longer in configuration.
An active watcher, registration, or CLI update blocks removal. Agent Homes,
credentials, account configuration, local maintenance history, and shared PATH
entries remain intact. `--reason` saves the reason locally in each registered
Home's `uninstall.json`; its text is never uploaded. Optional fixed reason
categories may be included in uninstall telemetry.

On Unix, apply removes the running executable and its exact `.previous`,
`.update.json`, and `.install.json` sidecars. Sidecars must be regular files;
symlinked or directory-shaped sidecars block removal. Installation/update/watch
lock files are retained deliberately: unlinking a shared lock path allows
another process to recreate it while an existing owner still holds its inode.
No glob-based cleanup or recursive Agent Home deletion is performed.

On Windows, the CLI reports `executable_removal_pending` and an explicit
`cleanup_files` list. `static/uninstall.ps1` waits for the CLI to exit and copies
it to a temporary reporter. Finalization reacquires installation/watch locks,
checks file hashes against the receipt, removes unchanged originals and records
the result. The wrapper then removes the temporary copy. Direct CLI invocation
leaves removal pending. Native Windows acceptance remains required.

Registration distributes the embedded official wrapper beside the binary and
records its digest. Edited wrappers are preserved. `--begin` records intent
before native host cleanup; continue through `--attempt`. Native cleanup failure
can be recorded with `--record-result blocked` or `failed`. Telemetry uses the
existing Agent maintenance endpoint and event table, with `phase=uninstall` and
results `started`, `removed`, `partial`, `failed`, `blocked`. Upload failure does
not block removal; there is no guaranteed background delivery after uninstall.

Notification cleanup is opt-in with `--notifications` and requires
`--notification-cleanup-confirmed` for all shared users. Unknown or edited assets
are preserved. OS notification permission records are not modified.

Skills removal is opt-in with `--skills`. The independent
`skills uninstall --into PATH` command also previews by default and requires
`--apply` to remove files. A target can be shared by several hosts or accounts;
review those owners before requesting removal. Only directories named in an
EigenFlux-managed manifest with an unchanged synchronization hash are removed.
Edited directories, unrelated files, and symlinked skill roots are retained.
A symlinked target or manifest is rejected. Provisional installations without a
managed manifest are not treated as managed content. The manifest remains when
edited content is preserved. Installation-wide removal also retains it as
ownership evidence, allowing retries after a later target or executable fails
without accepting arbitrary unmanaged directories.

Verification uses a separately compiled CLI copied into isolated temporary
fixtures. Tests never invoke uninstall against the developer's installed CLI
or the Go test executable. Coverage includes preview immutability, shared Home
confirmation, active and removed-server watch locks, update contention, local
reason persistence, executable/sidecar removal, managed Skills integrity, and
the POSIX installer's already-current executable path.
