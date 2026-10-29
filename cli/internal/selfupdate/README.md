# Signed CLI package updates

Keep this map current with package and activation changes.

`manifest.go` authenticates the stable version and exact bare binary/package
hashes using the compiled Ed25519 key. Legacy `release.json` keeps its original
fields and signature; complete clients use separately signed
`package-release.json`. `package.go` stages immutable complete
packages in `<stable executable>.versions/<version>` without replacing running
files. Package-aware clients require the signed ZIP; they never fall back to
an incomplete bare executable. Bare artifacts remain available for older clients.

ZIPs contain `eigenflux` (`eigenflux.exe` on Windows) and, on macOS, the paired
`EigenFlux Notifications.app`. Validate the ZIP hash, portable names, regular
entry types, case collisions, entry/expanded-size limits, every extracted file,
the separately signed CLI hash, its version probe, and the macOS helper signature.
Retain the signed ZIP for full verification when reusing an existing version.
Never overwrite existing versions, remove quarantine, request notification
permission, or change credentials during staging. Probe before pointer activation;
staging alone is not successful adoption. Runtime business failures never replay
work on an older version. A bare same-version CLI bootstraps its complete package
once despite the old daily throttle. Failed or interrupted checks retry after
five minutes; successful checks keep the 24-hour interval. Preserve the verified
target and fixed decision reason separately from raw local error diagnostics.

`cli/scripts/cli-packages.py` creates deterministic ZIPs before `clirelease`
signs their final hashes. Build macOS helpers on macOS for each target CPU.
Publish immutable versioned payloads before changing the signed latest pointer.
