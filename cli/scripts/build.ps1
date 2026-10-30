param(
    [switch]$TestBundle,
    [ValidateSet('amd64', 'arm64')][string]$Arch = '',
    [string]$OutDir = '',
    [string]$VerifyPublicKey = $env:EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY
)
$ErrorActionPreference = 'Stop'
$cliDir = Split-Path $PSScriptRoot -Parent
Push-Location $cliDir
try {
    $buildArgs = @('run', './cmd/devbuild')
    if ($TestBundle) { $buildArgs += '-test-bundle' }
    if ($Arch) { $buildArgs += @('-target', "windows/$Arch") }
    if ($OutDir) { $buildArgs += @('-out', $OutDir) }
    if ($VerifyPublicKey) { $buildArgs += @('-public-key', $VerifyPublicKey) }
    & go @buildArgs
    if ($LASTEXITCODE -ne 0) { throw "CLI build failed (exit $LASTEXITCODE)." }
} finally {
    Pop-Location
}
