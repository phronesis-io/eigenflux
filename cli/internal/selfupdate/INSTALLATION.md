# Packaged installation entry

Update this file whenever installation, activation, or process handoff changes.

- Preserve the stable CLI entry. Store releases in `<entry>.versions/<version>/`, containing `eigenflux[.exe]` and signed `release.json`.
- The entry retains its first package-capable implementation. New releases do not replace it; incompatible future launcher protocols require migration.
- A manually installed newer stable entry never forwards to an older active version; reading its version leaves the existing pointer unchanged.
- Verify and probe the complete package before `ActivateInstallation`. Accounts share installation locks and an atomic `<entry>.current.json` pointer. Reject downgrades and corrupt existing pointers.
- A package-capable bare CLI may bootstrap the same version once despite a legacy daily check. Persist `package_checked` so failed bootstrap attempts remain throttled.
- Pointers contain canonical versions only. Verify the signature, version, directory, and CLI digest before launching; reject arbitrary paths and symlink files.
- Accept `EIGENFLUX_INSTALL_ROOT` only for the stable entry or its exact version layout. Callers pin Home, server, and identity; updates preserve credentials.
- `ForwardInstallation` uses exec on Mac. The generic Windows entry preserves child exit codes. The first watch adoption establishes an account-pinned launcher; drained children exit 75 to adopt a higher active version. Parent depth stays bounded.
- Convert `InstallationRestart` to exit 75 only for managed watch children. Report failed or non-advancing adoption explicitly.
- Derive the first bare watch's root from its running executable, then validate the target release path; never treat the new version path as a new installation root.
- A validated internal `EIGENFLUX_INSTALL_CHILD=1` invocation of the running entry skips forwarding, preserving admitted bare-watch work until handoff. Normal launches still adopt the active release.
- Verification or staging failure preserves the old entry, release, and pointer. Staging or activation alone does not prove that watch adopted the release.
