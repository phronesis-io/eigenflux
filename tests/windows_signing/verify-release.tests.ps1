$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$verifier = Join-Path $PSScriptRoot '../../cli/scripts/verify-windows-release.ps1'
$testDir = Join-Path ([System.IO.Path]::GetTempPath()) ([guid]::NewGuid().ToString())
New-Item -ItemType Directory $testDir | Out-Null
try {
    $names = @('eigenflux-windows-amd64.exe', 'eigenflux-windows-arm64.exe')
    foreach ($name in $names) { Set-Content (Join-Path $testDir $name) 'unsigned fixture' }
    # Exercise the actual Windows signature API before introducing test doubles.
    $failed = $false
    try { & $verifier -BuildDirectory $testDir -ExpectedSubject 'CN=Test publisher' } catch { $failed = $true }
    if (-not $failed) { throw 'Unsigned binaries were accepted' }

    # Round-trip a real, publicly signed executable supplied by the Windows runner.
    $signedSource = Join-Path $PSHOME 'pwsh.exe'
    $sourceSignature = Get-AuthenticodeSignature -LiteralPath $signedSource
    if ($sourceSignature.Status -ne 'Valid' -or $null -eq $sourceSignature.TimeStamperCertificate) {
        throw 'The runner must supply a trusted timestamped pwsh.exe fixture'
    }
    foreach ($name in $names) { Copy-Item $signedSource (Join-Path $testDir $name) -Force }
    & $verifier -BuildDirectory $testDir -ExpectedSubject $sourceSignature.SignerCertificate.Subject
    Remove-Item (Join-Path $testDir '*.sha256')
    $tampered = Join-Path $testDir $names[1]
    $bytes = [IO.File]::ReadAllBytes($tampered)
    $bytes[64] = $bytes[64] -bxor 1
    [IO.File]::WriteAllBytes($tampered, $bytes)
    $failed = $false
    try { & $verifier -BuildDirectory $testDir -ExpectedSubject $sourceSignature.SignerCertificate.Subject } catch { $failed = $true }
    if (-not $failed) { throw 'Tampered signed binary was accepted' }
    if (@(Get-ChildItem $testDir -Filter '*.sha256').Count -ne 0) { throw 'Tampered release emitted checksums' }

    # Isolate trust-result handling; these doubles do not establish public trust.
    function Get-AuthenticodeSignature {
        param([string]$LiteralPath)
        $isArm = $LiteralPath.EndsWith('arm64.exe')
        [pscustomobject]@{
            Status = $(if ($script:scenario -eq 'invalid' -and $isArm) { 'HashMismatch' } else { 'Valid' })
            SignatureType = $(if ($script:scenario -eq 'catalog') { 'Catalog' } else { 'Authenticode' })
            SignerCertificate = [pscustomobject]@{ Subject = $(if ($script:scenario -eq 'publisher') { 'CN=Other' } else { 'CN=Test publisher' }) }
            TimeStamperCertificate = $(if ($script:scenario -eq 'timestamp') { $null } else { [pscustomobject]@{ Subject = 'CN=Timestamp' } })
        }
    }
    foreach ($script:scenario in @('invalid', 'catalog', 'publisher', 'timestamp', 'valid')) {
        $failed = $false
        try { & $verifier -BuildDirectory $testDir -ExpectedSubject 'CN=Test publisher' } catch { $failed = $true }
        if ($failed -ne ($script:scenario -ne 'valid')) { throw "Unexpected result: $script:scenario" }
        if ($script:scenario -ne 'valid' -and @(Get-ChildItem $testDir -Filter '*.sha256').Count -ne 0) {
            throw 'A failed verification emitted checksums'
        }
    }
    foreach ($name in $names) {
        $path = Join-Path $testDir $name
        if ([IO.File]::ReadAllText("$path.sha256") -cne ((Get-FileHash $path -Algorithm SHA256).Hash.ToLowerInvariant() + "`n")) {
            throw 'Checksum does not describe final binary bytes'
        }
    }
    Write-Host 'Signature policy and final checksum tests passed'
} finally {
    Remove-Item -Recurse -Force $testDir
}
