# CLI release integrity and Windows verification

The CLI release builds six binaries (Linux, macOS and Windows, each for amd64
and arm64). `cli/scripts/cli-artifacts.py generate build/cli` writes a bare,
lowercase SHA-256 digest plus newline to each `<binary>.sha256` file. Python 3
is required by the build and publication scripts. Generate checksums after any
operation that changes the binary, including future Authenticode signing.

`publish.sh` checks all six binaries and their checksums, and requires the build
version to match `cli/.cli.config`, before uploading anything. Immutable payloads,
checksums and both manifest schemas are uploaded under `cli/<version>/` with
conditional creation. Existing objects must match exactly. CDN verification
precedes latest-pointer promotion. Installers resolve the v2 version pointer
once and download from the immutable version directory; only a 404 permits
legacy fallback. Legacy aliases advance only when CLI and notifier versions
match. Latest aliases are not atomic. A partial upload can remain after failure;
retry the same build, or bump the version if any published bytes changed.

The Windows installer requires a valid checksum before replacing the installed
CLI. Missing, invalid or mismatched checksums stop installation and remove the
temporary download; they do not overwrite the existing CLI. Other downloads
that do not supply a checksum URL retain their existing behavior.

## Rollout

Publish and verify CLI binaries with their checksum files **before** deploying
the stricter `static/install.ps1`. The PowerShell installer is served by the
backend; the independent shell-installer workflow does not publish it. Existing
releases without checksum files cannot be installed by this stricter installer.
This code change does not backfill or modify already-published releases.

## Windows downloads and verification

The official artifact base is `https://cdn.eigenflux.ai/cli/`. Resolve the version
once, then use the same version and architecture for both downloads:

```powershell
$version = (Invoke-RestMethod 'https://cdn.eigenflux.ai/cli/latest/version.txt').Trim()
$arch = 'amd64' # Use arm64 for Windows on ARM.
$base = "https://cdn.eigenflux.ai/cli/$version/eigenflux-windows-$arch.exe"
Invoke-WebRequest $base -OutFile eigenflux.exe -UseBasicParsing
Invoke-WebRequest "$base.sha256" -OutFile eigenflux.exe.sha256 -UseBasicParsing
$expected = (Get-Content ./eigenflux.exe.sha256 -Raw).Trim()
$actual = (Get-FileHash ./eigenflux.exe -Algorithm SHA256).Hash
if ($expected -notmatch '^[a-fA-F0-9]{64}$' -or $actual -ne $expected) {
    throw 'CLI SHA256 verification failed'
}
Get-AuthenticodeSignature ./eigenflux.exe |
    Format-List Status, StatusMessage, SignerCertificate, TimeStamperCertificate
```

Checksums establish byte integrity, not Windows publisher trust. Current CLI
releases do not have Authenticode signatures. The Ed25519 signature of the
Skills manifest is a separate mechanism. Do not describe a matching checksum
as a fix for Smart App Control (SAC) or recommend disabling SAC.

Closing issue #385 additionally requires a publicly trusted Windows signing
identity, timestamped signatures on both Windows architectures, signature
verification before checksum generation, and execution evidence on Windows 11
with SAC enforcement enabled. Record the tested artifact hash, Windows build,
SAC mode, signature status and relevant CodeIntegrity events. Ordinary GitHub
Windows runners and cross-compilation do not establish SAC acceptance.

See Microsoft's [code-signing guidance](https://learn.microsoft.com/en-us/windows/apps/develop/smart-app-control/code-signing-for-smart-app-control)
and [SAC testing guidance](https://learn.microsoft.com/en-us/windows/apps/develop/smart-app-control/test-your-app-with-smart-app-control).

## macOS shell bootstrap

`static/install.sh` verifies the downloaded bootstrap binary checksum before
execution, then calls `eigenflux --homedir <resolved-home> update`. The CLI uses
its compiled trust key to verify and activate the complete signed package,
including the notification helper. The same step runs for an already installed
matching version, repairing bare installations without bypassing package checks.
A failed complete-package installation stops onboarding and notification setup;
it must not be reported as a successful installation. Linux keeps its existing
binary installation path. Identity-directory selection remains host-scoped.

## Release ownership and credentials

Use the existing `Release CLI` workflow in `phronesis-io/eigenflux`, dispatched
from `main` after the release PR is merged. Repository Actions Secrets hold the
existing Skills signing key and R2 credentials plus `MACOS_CERTIFICATE_P12_BASE64`,
`MACOS_CERTIFICATE_PASSWORD`, `MACOS_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_TEAM_ID`,
and `APPLE_APP_SPECIFIC_PASSWORD`. Maintainers with workflow-run permission use
the same entry; they do not need to receive private keys or passwords.

Run publication tests before importing Apple credentials. Import the encrypted
P12 and its G2 certificate chain into a temporary runner keychain, sign with
Developer ID Application, notarize the Intel and Apple Silicon notification
helpers concurrently, staple and validate both tickets, then publish verified
complete packages only after both jobs succeed. Each newly built helper must be
notarized before distribution, including beta releases; notarization is not a
one-time account approval. Never commit
credentials or include them in distributable archives. Always remove temporary
keychain, P12 and signing-key files. Do not enable a second DevOps publisher.

The first stable outer-loop release uses `CLI_VERSION=1.0.0` and
`SKILLS_MIN_CLI_VERSION=1.0.0`. Production clients upgrade normally; higher
numbered local test builds require an explicit owner installation of the stable
bundle. Preserve automatic downgrade protection and the existing signing key.

## Notification App releases

`NOTIFIER_VERSION` is independent of `CLI_VERSION` after the signed-channel
bridge. The bridge release must use the same CLI and notification versions;
older CLIs validate that pair and cannot directly install independent App
versions. Its original `release.json` and `package-release.json` remain signed
with the unchanged v1 contract. Once independent releases begin, keep those
legacy pointers at the bridge so older installations can adopt the new updater
before following the v2 channel on their next check.

New clients first request `/cli/v2/latest/release.json` or the complete-package
`package-release.json`. Only HTTP 404 permits fallback to the original
`/cli/latest/` manifest. A present v2 manifest requires the signed
`notifier_version` field and the `eigenflux_cli_release.v2` signature domain.
Transport errors, non-404 responses, bad signatures and malformed manifests
fail closed without trying the legacy lane. Existing rollback protection stays
active. Versioned artifact URLs remain `/cli/<CLI version>/<artifact>`.

The original stable CLI executable remains on disk during complete-package
upgrades and still verifies the selected version's `release.json` using v1.
For a v2 package, the updater therefore also downloads the independently signed
versioned `/cli/<version>/release.json`, requires a v1 bare manifest with the
same CLI version and exact executable artifact set, and retains it as the
installation directory's `release.json`. The signed v2 complete-package
manifest is retained separately as `.package-release.json`; package rechecks
validate both retained metadata files and the exact archive contents. Reject
missing, invalid, or inconsistent stable-entry metadata before activation.
This keeps an original v1 stable launcher able to forward to newer CLI
versions after the bridge without weakening App package validation.


Shell and PowerShell installers select `/cli/v2/latest/version.txt` before the
legacy `/cli/latest/version.txt` bridge. Only HTTP 404 permits fallback; invalid
versions, network failures and other HTTP statuses stop installation without
replacing the installed CLI. The new bootstrap CLI then verifies its signed
complete-package manifest through the same channel selection.

`clirelease --notifier-version <version>` generates the v2 payloads as
`release-v2.json` and `package-release-v2.json`, alongside the two unchanged v1
files. The publisher places them at the corresponding v2 manifest URLs. The
complete archive hash authenticates both CLI and App contents; package
activation checks both App plist version fields against `notifier_version`
(or the CLI version for legacy manifests), then verifies the App signature.

Notification App source or build-input changes require a notification version
bump. Exact unchanged signed/notarized notification releases are reused from
the immutable notifier release store. CLI assembly checks archive structure,
App signature, expected Developer ID team and manifest version locally; it does
not run Apple online ticket or Gatekeeper queries again. `APPLE_TEAM_ID` is
required by the publication check. The notifier publication workflow alone
performs the full online notarization, ticket and Gatekeeper acceptance before
making a reusable release available. Skills-only releases do not build the App.

CLI cross-compilation runs in bounded batches of three platforms. Each compiler writes a separate build log. All children are waited for; any failure stops the build before Skills staging or package generation.

## Durable notification checkpoints

The official store is R2, not an expiring Actions cache. Its identity is
`notifier/<NOTIFIER_VERSION>/<source SHA-256>/`. The source hash covers native
macOS notification sources and the native build script. `ready.json` is an
Ed25519-signed receipt binding the version, source hash, Developer ID team and
both architecture archives. It is created after immutable archives are stored.
Restore validates the receipt, archive hashes, safe paths, local code signatures,
architecture and versions. Only an absent ready object is a miss; authentication,
transport and validation errors abort rather than building a replacement.
After both Apps pass validation, restore retains the exact receipt-bound ZIPs
beside `ready.json` for the later offline package-publication check.

A miss builds/signs both architectures concurrently. R2 staging preserves each
signed archive and Apple submission ID separately. Later runners reuse those
bytes and resume `notarytool wait`, then staple. New publication performs ticket
and Gatekeeper acceptance; only transient network failures receive bounded
retries. Invalid signatures or rejected submissions stop immediately. A network
loss between Apple accepting a submission and the ID being persisted remains
an external-service ambiguity; do not claim exactly-once submission in that gap.

Partial official uploads are recoverable across runners: an existing archive
may be adopted only when every extracted member, permission and content hash
matches the newly validated App, including ticket metadata. Container timestamps
and compression may differ. The existing immutable bytes then enter the receipt;
payload or permission changes fail without overwriting remote objects.

For this bridge release set CLI and notifier to `1.0.5`. Later CLI-only releases
keep the notifier version and source unchanged and restore the ready package
without Apple credentials, ticket queries or Gatekeeper queries. Bump the native
version whenever native source/build inputs change. Keep the legacy lane at the
last compatible bridge after versions diverge. Existing legacy clients may need
a second update check after installing the bridge to reach a later v2 release.

Publication also requires the local `ready.json` and its two notification ZIPs
from `build/notifier-formal/work` (`EIGENFLUX_NOTIFIER_RELEASE_DIR` overrides
this directory for manual publication). The same pre-upload guard verifies the
receipt signature, version, team, archive digests and every packaged App file
and normalized permission offline. Missing or mismatched proof stops publication;
a Developer ID signature alone cannot authorize distribution. No extra Apple
query or receipt format is introduced.

## Release orchestration and retries

CLI and Skills each serialize their own workflow using the existing GitHub
concurrency policy.
Preparation jobs do not hold `release-r2`: tests, compilation, native signing,
notarization and bundle generation can proceed independently. Only publication
jobs share that group, covering final origin checks, uploads, pointer promotion
and delivery verification. They never cancel an in-progress publication.

A push that changes `CLI_VERSION` defers Skills publication until that CLI
workflow succeeds. Test-only changes do not trigger a Skills release. An older
CLI success event cannot publish the Skills for a newer, still-pending CLI
version. A Skills run selects the current main SHA once. Preparation and
publication check out that exact SHA; an old CLI rerun cannot select obsolete
Skills sources. Missing history causes conservative deferral rather than
premature publication.

Release planning may reuse a successful `CLI Auth Cross-Platform` main-push run
for the exact repository and source SHA. PR-head success, a different SHA,
incomplete/failed results or unavailable GitHub evidence never authorize a
skip. Uncovered release-tool tests remain mandatory; macOS native publication
checks always run. Artifact manifests receive a fresh signature check before
CLI publication even when earlier CI is reused.

Prepared files travel as tar archives inside current-run GitHub artifacts to
preserve executable permissions. Retention is 14 days. The CLI source marker
and Skills prepared-source record must match the publication checkout. Signing
keys remain in the preparation runner's temporary directory and are not archived;
publication jobs receive verification keys and R2 credentials only.

For a failed publication job, rerun failed jobs on the original workflow run.
The successful preparation job and exact signed bytes are reused; do not rebuild
or resign a partially published version. Existing immutable objects must match
byte-for-byte. A Skills retry may resume its exact reserved signed manifest;
a newer origin sequence or a conflicting artifact aborts instead of rolling back.

Skills publication compares the fully verified origin aliases and prepared
content. Identical releases retain the existing signed manifest and sequence (including
its informational producer CLI tag),
while still verifying payloads, client Sync and installation-document delivery.
An inconsistent origin cannot use this no-op path.

CLI preflight, immutable uploads, alias transfers and CDN verification use bounded
parallelism (default 3, `EIGENFLUX_RELEASE_WORKERS` accepts 1 through 8).
All preflight checks finish before any write; all started workers finish before
failure returns. Pointer promotion remains ordered and follows complete payload
verification. Retries skip existing identical immutable and alias payloads.

### Overlapping CLI and Skills preparation

Release Skills starts when Release CLI enters `in_progress` and freezes current
main as its source SHA after checking that the CLI version matches the upstream
run. Skills tests, signing and artifact preparation overlap CLI work.
A separate job waits for that exact CLI run to succeed before Skills publication;
the wait does not hold `release-r2`. A failed or cancelled CLI blocks publication.
After recovery, rerun Release CLI or dispatch Release Skills on main.

CI evidence is checked immediately before fallback tests, after unconditional
release-specific checks. Only completed successful main-push CI for the exact
source SHA may suppress covered tests. Pending CI still requires fallback tests;
starting a release before CI finishes does not establish reusable evidence.

## Doctor release diagnostics

Doctor reads `cli/v2/latest/version.txt`, matching the installer release lane,
and falls back to the legacy pointer only on HTTP 404. It reports a stable CLI
as outdated only when its numeric version is lower than the published version.
An equal or newer version does not produce an upgrade warning.
