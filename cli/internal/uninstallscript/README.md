# Official uninstall wrappers

Keep embedded wrappers byte-identical to `static/uninstall.sh` and
`static/uninstall.ps1`; the contract test enforces this. Update this file and
`cli/cmd/UNINSTALL.md` with behavior changes.

Installation registration distributes the wrapper beside the resolved binary
and records its digest. Test bundles also include both standalone wrappers.
Use the absolute binary path as the first argument; pass CLI flags as data.
Protocol 1 requires CLI 0.6.3. No script downloads or arbitrary shell expansion
are needed. Preview performs no cleanup. Windows retains a temporary trusted CLI
for locked original-file removal and final telemetry, then deletes the copy.

CLI 0.6.4 finalization clears owned local data and reports `local_data_removed`.
The Windows wrapper reports data removal only when the finalizer confirms it;
older protocol-1 CLIs retain their existing data-preservation behavior.
Partial finalization prints the actual local-data cleanup state and returns a
nonzero exit code. Home cleanup can succeed while an abnormal wrapper remains.
