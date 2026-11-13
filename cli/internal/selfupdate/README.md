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
Manifest requests use a 30-second timeout. Binary and ZIP downloads use up to
90 seconds and reserve 30 seconds of the update deadline for validation,
probing and activation.

`cli/scripts/cli-packages.py` creates deterministic ZIPs before `clirelease`
signs their final hashes. Build macOS helpers on macOS for each target CPU.
Publish immutable versioned payloads before changing the signed latest pointer.

`installation_cleanup.go` supplies complete-uninstall ownership inventories using
the same signed ZIP and extracted-file validation. Current-Agent removal retains
the shared packages. Complete removal validates the receipt again under
installation/update/activation locks and removes verified files and empty
directories without recursive deletion. Unknown or modified content stays intact.

## Failure observations

`diagnostics.go` classifies wrapped network and filesystem errors into bounded
maintenance `error_code` values. Download failures distinguish HTTP status,
timeout, DNS, TLS and incomplete transfers. Installation failures retain the
backup, replace, package staging/rename, activation or pointer write/sync step
and permission, file-in-use, disk-full or other system category. Other native
errors retain the OS and numeric errno; unclassified errors remain `failed`.
Local errors retain their original detail. Rollback outcomes
remain authoritative and retain their diagnostic code. Classification does not
change locks, trust checks, retry intervals or installation operations.

The existing maintenance wire format and backend accept these codes without a
migration. Raw paths, URLs, response bodies and credentials are never encoded
in a code. Coverage starts when the client runs this implementation; upgrading
a client cannot enrich failures emitted by its previous executable.

## Native install retries and validation diagnostics

Only the final Windows package directory rename retries access-denied,
sharing-violation and lock-violation errors. Reuse the verified staging directory
for at most two seconds, checking cancellation between attempts. Persistent
refusal retains the original system error, old release and active pointer.
Other errors and other operating systems retain a single rename attempt.
No ACL, quarantine, signature requirement or update interval changes.

macOS helper validation distinguishes plist command failure, version mismatch
and signature verification failure in the existing maintenance error code.
Capture at most 2 KiB from each command stream for local diagnostics; raw tool
errors never enter maintenance events. Tool cancellation/deadline remains
visible through the wrapped error. A restricted security service can cause
verification failure; that failure never authorizes installation or bypasses
`codesign --verify --deep --strict`.

The cross-platform CLI workflow runs this entire package natively on Windows,
macOS and Linux. Windows cases use real file handles without delete sharing to
exercise temporary denial, persistent denial, cancellation, signed activation
and preservation of the old installation. macOS cases exercise real plist and
signature verification failures against isolated signed fixtures.
