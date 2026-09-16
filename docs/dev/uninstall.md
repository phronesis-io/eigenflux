# CLI installation removal

`eigenflux uninstall` previews the installation registered beside the running
executable. It performs no network requests and does not write files during
preview, including when Skills inspection is requested.

The installers register the actual executable selected or installed, the
normalized Agent Home, the invoking host, and the Skills target they used.
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
Home's `uninstall.json`; it is never uploaded by this operation.

On Unix, apply removes the running executable and its exact `.previous`,
`.update.json`, and `.install.json` sidecars. Sidecars must be regular files;
symlinked or directory-shaped sidecars block removal. Installation/update/watch
lock files are retained deliberately: unlinking a shared lock path allows
another process to recreate it while an existing owner still holds its inode.
No glob-based cleanup or recursive Agent Home deletion is performed.

On Windows, the CLI reports `executable_removal_pending` and an explicit
`cleanup_files` list. `static/uninstall.ps1` waits for the CLI to exit, validates
the executable and sidecar paths, then removes them. Running the CLI directly
on Windows leaves removal pending. The PowerShell path is implemented but must
be validated on a native Windows host before claiming full Windows acceptance.

Skills removal is opt-in with `--skills`. The independent
`skills uninstall --into PATH` command also previews by default and requires
`--apply` to remove files. A target can be shared by several hosts or accounts;
review those owners before requesting removal. Only directories named in an
EigenFlux-managed manifest with an unchanged synchronization hash are removed.
Edited directories, unrelated files, and symlinked skill roots are retained.
A symlinked target or manifest is rejected. Provisional installations without a
managed manifest are not treated as managed content. The manifest remains when
edited content is preserved.

Verification uses a separately compiled CLI copied into isolated temporary
fixtures. Tests never invoke uninstall against the developer's installed CLI
or the Go test executable. Coverage includes preview immutability, shared Home
confirmation, active and removed-server watch locks, update contention, local
reason persistence, executable/sidecar removal, managed Skills integrity, and
the POSIX installer's already-current executable path.
