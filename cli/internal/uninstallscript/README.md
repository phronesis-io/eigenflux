# Official uninstall wrappers

Keep embedded wrappers byte-identical to `static/uninstall.sh` and
`static/uninstall.ps1`; the contract test enforces this. Update this file and
`cli/cmd/UNINSTALL.md` with behavior changes.

Installation registration distributes the wrapper beside the resolved binary
and records its digest. Test bundles also include both standalone wrappers.
Use the absolute binary path as the first argument; pass CLI flags as data.
Protocol 1 requires CLI 1.0.2. New versioned installations report the actual
version-child reporter path and SHA-256. The Windows wrapper validates its
installation-relative path and hashes both source and temporary copy using .NET SHA-256 directly (compatible
with a PowerShell 5.1 child inheriting PowerShell 7 module paths), so an old
stable launcher cannot finalize a newer package-cleanup receipt. No script downloads or arbitrary shell expansion
are needed. Preview performs no cleanup. Windows retains a temporary trusted CLI
for locked original-file removal and final telemetry, then deletes the copy on success. On failure it retains the verified reporter
and prints its recovery command; recovery is independent of the stable launcher.

CLI 0.6.4 finalization clears owned local data and reports `local_data_removed`.
The Windows wrapper reports data removal only when the finalizer confirms it;
older protocol-1 CLIs retain their existing data-preservation behavior.
Partial finalization prints the actual local-data cleanup state and returns a
nonzero exit code. Home cleanup can succeed while an abnormal wrapper remains.

Only temporary Windows reporter subprocesses clear `EIGENFLUX_INSTALL_ROOT`,
`EIGENFLUX_INSTALL_CHILD` and `EIGENFLUX_INSTALL_WATCH_LAUNCHER` from their child
environment. Preserve the parent and original stable-entry command environment.
The POSIX wrapper has no temporary reporter and retains the installation context.

Interrupted package removal can resume using the same receipt for 24 hours.
On POSIX, use an independent official signed CLI with the same verification key:
clear installation forwarding variables and run `--homedir <receipt-home>
uninstall-finalize --attempt <id> --remove-original`. Do not rely on a stable
entry whose active version may already have been removed. Revalidation preserves
unknown or changed files. Remove a retained recovery reporter after success.
