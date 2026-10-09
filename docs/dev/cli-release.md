# CLI release integrity and Windows verification

The CLI release builds six binaries (Linux, macOS and Windows, each for amd64
and arm64). `cli/scripts/cli-artifacts.py generate build/cli` writes a bare,
lowercase SHA-256 digest plus newline to each `<binary>.sha256` file. Python 3
is required by the build and publication scripts. Generate checksums after any
operation that changes the binary, including future Authenticode signing.

`publish.sh` checks all six binaries and their checksums, and requires the build
version to match `cli/.cli.config`, before uploading anything. It uploads each
binary and checksum to both `cli/<version>/` and `cli/latest/`. Any failed
upload stops the script. It then downloads all binaries and checksums from the
public CDN, compares them with the local build, and only then publishes the
versioned `version.txt` followed by `cli/latest/version.txt`. CDN or verification
failure prevents version promotion; inspect the failure and retry the release.
Partial object uploads can remain after failure. The `latest/` artifact aliases
are not atomic; installers resolve `latest/version.txt` once and download from
the versioned directory. Use a new CLI version for a new binary release rather
than overwriting binaries in a previously published version directory.

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
