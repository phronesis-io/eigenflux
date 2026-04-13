# ============================================================
# EigenFlux CLI Installer for Windows
# Usage: irm https://www.eigenflux.ai/install.ps1 | iex
# ============================================================

$ErrorActionPreference = "Stop"

$CdnUrl = if ($env:EIGENFLUX_CDN_URL) { $env:EIGENFLUX_CDN_URL } else { "https://cdn.eigenflux.ai" }

function Info($msg)  { Write-Host $msg -ForegroundColor Cyan }
function Ok($msg)    { Write-Host $msg -ForegroundColor Green }
function Err($msg)   { Write-Host $msg -ForegroundColor Red; exit 1 }

# Detect architecture
$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    "X64"   { "amd64" }
    "Arm64" { "arm64" }
    default { Err "Unsupported architecture: $_" }
}

$binName = "eigenflux-windows-${arch}.exe"
Info "Detected: windows/${arch}"

# Fetch latest version
try {
    $latestVersion = (Invoke-RestMethod -Uri "${CdnUrl}/cli/latest/version.txt").Trim()
} catch {
    Err "Failed to fetch latest version from ${CdnUrl}"
}
Info "Latest version: ${latestVersion}"

# Check if already installed
$currentVersion = $null
$eigenfluxCmd = Get-Command eigenflux -ErrorAction SilentlyContinue
if ($eigenfluxCmd) {
    try { $currentVersion = (& eigenflux version --short 2>$null).Trim() } catch {}
    if ($currentVersion -eq $latestVersion) {
        Ok "eigenflux ${currentVersion} is already up to date."
        exit 0
    }
    Info "Upgrading eigenflux ${currentVersion} -> ${latestVersion}"
} else {
    Info "Installing eigenflux ${latestVersion}"
}

# Download
$downloadUrl = "${CdnUrl}/cli/${latestVersion}/${binName}"
$installDir = Join-Path $env:LOCALAPPDATA "local\bin"
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}
$installPath = Join-Path $installDir "eigenflux.exe"

Info "Downloading ${downloadUrl}..."
Invoke-WebRequest -Uri $downloadUrl -OutFile $installPath -UseBasicParsing

# Add to PATH if not already present
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
    $env:Path = "$env:Path;$installDir"
    Info "Added ${installDir} to user PATH"
}

Ok "eigenflux ${latestVersion} installed successfully"
try { & $installPath version } catch {}

Ok ""
Ok "Done! Restart your terminal, then run 'eigenflux --help' to get started."
