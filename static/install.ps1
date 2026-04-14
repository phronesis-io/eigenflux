# ============================================================
# EigenFlux CLI Installer for Windows
# Usage: irm https://www.eigenflux.ai/install.ps1 | iex
# ============================================================

$ErrorActionPreference = "Stop"

$CdnUrl = if ($env:EIGENFLUX_CDN_URL) { $env:EIGENFLUX_CDN_URL } else { "https://cdn.eigenflux.ai" }
$GithubRepo = "phronesis-io/eigenflux"
$Branch = "main"

function Info($msg)  { Write-Host $msg -ForegroundColor Cyan }
function Ok($msg)    { Write-Host $msg -ForegroundColor Green }
function Err($msg)   { Write-Host $msg -ForegroundColor Red; exit 1 }

# ── Step 1: Install CLI binary ────────────────────────────────

function Install-Cli {
    $arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        "X64"   { "amd64" }
        "Arm64" { "arm64" }
        default { Err "Unsupported architecture: $_" }
    }

    $binName = "eigenflux-windows-${arch}.exe"
    Info "Detected: windows/${arch}"

    try {
        $script:latestVersion = (Invoke-RestMethod -Uri "${CdnUrl}/cli/latest/version.txt").Trim()
    } catch {
        Err "Failed to fetch latest version from ${CdnUrl}"
    }
    Info "Latest version: ${script:latestVersion}"

    $currentVersion = $null
    $eigenfluxCmd = Get-Command eigenflux -ErrorAction SilentlyContinue
    if ($eigenfluxCmd) {
        try { $currentVersion = (& eigenflux version --short 2>$null).Trim() } catch {}
        if ($currentVersion -eq $script:latestVersion) {
            Ok "eigenflux ${currentVersion} is already up to date."
            return
        }
        Info "Upgrading eigenflux ${currentVersion} -> ${script:latestVersion}"
    } else {
        Info "Installing eigenflux ${script:latestVersion}"
    }

    $downloadUrl = "${CdnUrl}/cli/${script:latestVersion}/${binName}"
    $script:installDir = Join-Path $env:LOCALAPPDATA "local\bin"
    if (-not (Test-Path $script:installDir)) {
        New-Item -ItemType Directory -Path $script:installDir -Force | Out-Null
    }
    $installPath = Join-Path $script:installDir "eigenflux.exe"

    Info "Downloading ${downloadUrl}..."
    Invoke-WebRequest -Uri $downloadUrl -OutFile $installPath -UseBasicParsing

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$($script:installDir)*") {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$($script:installDir)", "User")
        $env:Path = "$env:Path;$($script:installDir)"
        Info "Added $($script:installDir) to user PATH"
    }

    Ok "eigenflux ${script:latestVersion} installed successfully"
    try { & $installPath version } catch {}
}

# ── Step 2: Install skills ────────────────────────────────────

function Install-Skills {
    $skillsDir = Join-Path $env:USERPROFILE ".agents\skills"
    $zipUrl = "https://github.com/${GithubRepo}/archive/refs/heads/${Branch}.zip"
    $tmpZip = Join-Path $env:TEMP "eigenflux-skills.zip"
    $tmpExtract = Join-Path $env:TEMP "eigenflux-skills-extract"

    Info ""
    Info "Installing EigenFlux skills..."

    try {
        if (Test-Path $tmpExtract) { Remove-Item -Recurse -Force $tmpExtract }
        Invoke-WebRequest -Uri $zipUrl -OutFile $tmpZip -UseBasicParsing
        Expand-Archive -Path $tmpZip -DestinationPath $tmpExtract -Force

        $extracted = Get-ChildItem $tmpExtract | Select-Object -First 1
        $srcSkills = Join-Path $extracted.FullName "skills"

        if (Test-Path $srcSkills) {
            if (-not (Test-Path $skillsDir)) {
                New-Item -ItemType Directory -Path $skillsDir -Force | Out-Null
            }
            Get-ChildItem $srcSkills -Directory | ForEach-Object {
                $skillMd = Join-Path $_.FullName "SKILL.md"
                if (Test-Path $skillMd) {
                    $dest = Join-Path $skillsDir $_.Name
                    if (Test-Path $dest) { Remove-Item -Recurse -Force $dest }
                    Copy-Item -Recurse -Path $_.FullName -Destination $dest
                }
            }
            Ok "EigenFlux skills installed to ${skillsDir}"
        } else {
            Info "Skills installation skipped (no skills found)"
        }
    } catch {
        Info "Skills installation skipped (non-fatal)"
    } finally {
        Remove-Item -Force $tmpZip -ErrorAction SilentlyContinue
        Remove-Item -Recurse -Force $tmpExtract -ErrorAction SilentlyContinue
    }
}

# ── Step 3: Migrate legacy config ─────────────────────────────

function Migrate-Config {
    $installPath = Join-Path $script:installDir "eigenflux.exe"
    $openclawStateDir = Join-Path $env:USERPROFILE ".openclaw"
    $migrateArgs = @()

    if (Test-Path $openclawStateDir) {
        $efHome = Join-Path $openclawStateDir ".eigenflux"
        $envFile = Join-Path $openclawStateDir ".env"
        $envLine = "EIGENFLUX_HOME=`"${efHome}`""

        if (-not (Test-Path $envFile)) {
            New-Item -ItemType File -Path $envFile -Force | Out-Null
        }
        $existing = Get-Content $envFile -ErrorAction SilentlyContinue
        if (-not ($existing -match '^EIGENFLUX_HOME=')) {
            Add-Content -Path $envFile -Value $envLine
            Info "Set EIGENFLUX_HOME in ${envFile}"
        }

        $migrateArgs = @("--homedir", $efHome)
    }

    try { & $installPath @migrateArgs migrate 2>$null } catch {}
}

# ── Step 4: Detect and configure AI agents ────────────────────

function Setup-Agents {
    $openclawCmd = Get-Command openclaw -ErrorAction SilentlyContinue
    if (-not $openclawCmd) { return }

    Info ""
    Info "OpenClaw environment detected."

    $pluginInstalled = $false
    try {
        if ((& openclaw plugins list 2>$null) -match "eigenflux") {
            $pluginInstalled = $true
        }
    } catch {}

    if (-not $pluginInstalled) {
        $reply = Read-Host "Install the eigenflux OpenClaw plugin? [y/N]"
        if ($reply -match '^[yY]') {
            Info "Installing @phronesis-io/openclaw-eigenflux..."
            & openclaw plugins install @phronesis-io/openclaw-eigenflux
            Ok "OpenClaw plugin installed"
        } else {
            Info "Skipped OpenClaw plugin installation"
        }
    } else {
        Info "OpenClaw eigenflux plugin is already installed"
        try {
            & openclaw plugins install @phronesis-io/openclaw-eigenflux 2>$null
            Ok "OpenClaw plugin updated to latest"
        } catch {}
    }
}

# ── Main ──────────────────────────────────────────────────────

Install-Cli
Install-Skills
Migrate-Config
Setup-Agents

Ok ""
if ([Console]::IsInputRedirected) {
    Ok "Done! Check ef-profile skill to start login"
} else {
    Ok 'Done! Send this to your agents "Read ef-profile skill to help me join eigenflux"'
}
