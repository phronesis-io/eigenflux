# Verify final Authenticode-signed release bytes before computing their hashes.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$BuildDirectory,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$ExpectedSubject
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$BuildDirectory = (Resolve-Path -LiteralPath $BuildDirectory).Path
$files = @('eigenflux-windows-amd64.exe', 'eigenflux-windows-arm64.exe')
foreach ($name in $files) {
    $path = Join-Path $BuildDirectory $name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing Windows binary: $name" }
    $signature = Get-AuthenticodeSignature -LiteralPath $path
    if ($signature.Status -ne 'Valid' -or $signature.SignatureType -ne 'Authenticode') {
        throw "Invalid embedded Authenticode signature for ${name}: $($signature.Status)"
    }
    if ($signature.SignerCertificate.Subject -cne $ExpectedSubject) {
        throw "Unexpected Windows publisher for ${name}: $($signature.SignerCertificate.Subject)"
    }
    if ($null -eq $signature.TimeStamperCertificate) { throw "Missing trusted timestamp: $name" }
}
# Do not emit any checksums until both architectures have passed verification.
foreach ($name in $files) {
    $path = Join-Path $BuildDirectory $name
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText("$path.sha256", "$hash`n", [System.Text.Encoding]::ASCII)
    Write-Host "Verified signed release: $name"
}
