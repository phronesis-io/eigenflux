# ============================================================
# EigenFlux CLI Installer for Windows
# Usage: irm https://www.eigenflux.ai/install.ps1 | iex
# ============================================================

param(
    [ValidatePattern('^(?-i:EF-[0-9A-Za-z]{8})?$')]
    [string]$Ref = "",
    [string]$Endpoint = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$CdnUrl = if ($env:EIGENFLUX_CDN_URL) { $env:EIGENFLUX_CDN_URL } else { "https://cdn.eigenflux.ai" }
$GithubRepo = "phronesis-io/eigenflux"
$Branch = "main"
$ExplicitEigenfluxHome = $env:EIGENFLUX_HOME
if (-not $Endpoint) {
    $Endpoint = if ($env:EIGENFLUX_API_URL) { $env:EIGENFLUX_API_URL } else { "https://www.eigenflux.ai" }
}
$Endpoint = $Endpoint.TrimEnd('/')

function Info($msg)  { Write-Host $msg -ForegroundColor Cyan }
function Ok($msg)    { Write-Host $msg -ForegroundColor Green }
function Err($msg)   {
    if ($script:observationComponent) { Write-InstallObservation component_install failed $script:observationComponent unknown_failure }
    Write-Host $msg -ForegroundColor Red; exit 1
}

function Get-InvokingHost {
    if ($env:INVOKING_HOST) { return $env:INVOKING_HOST }
    if ($env:EIGENFLUX_HOST) { return ($env:EIGENFLUX_HOST -split '/')[0] }
    if ($env:CLAUDECODE) { return "claude-code" }
    if ($env:CODEX_THREAD_ID -or $env:CODEX_SANDBOX) { return "codex" }
    return ""
}

function Resolve-EigenfluxHome {
    param([string]$ExplicitHome, [string]$InvokingHost)
    if ($ExplicitHome) { return $ExplicitHome }
    switch ($InvokingHost) {
        "codex" { return (Join-Path $env:USERPROFILE ".eigenflux-codex\.eigenflux") }
        "openclaw" { return (Join-Path $env:USERPROFILE ".openclaw\.eigenflux") }
        default { return (Join-Path $env:USERPROFILE ".eigenflux") }
    }
}

# Same pre-CLI queue as internal/installflow; observations never alter an install.
# Proof stays in a private file and in-memory HTTP headers, never external argv.
function Protect-ObservationDirectory([string]$Path) {
    if (Test-Path -LiteralPath $Path) {
        if ((Get-Item -LiteralPath $Path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Linked observation state' }
    } else { [IO.Directory]::CreateDirectory($Path) | Out-Null }
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true, $false)
    $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function New-ObservationHex([int]$Bytes) {
    $buffer = New-Object byte[] $Bytes
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($buffer) } finally { $rng.Dispose() }
    return ([BitConverter]::ToString($buffer)).Replace('-', '').ToLowerInvariant()
}

function Write-InstallObservation([string]$Type, [string]$Result, [string]$Component = '', [string]$Reason = '') {
    $temporary = $null
    try {
        if ($env:EIGENFLUX_INSTALL_OBSERVATIONS -eq '0') { return }
        if (@('install_started:started::', 'host_detected:succeeded::', 'component_install:started:cli:', 'component_install:succeeded:cli:', 'component_install:failed:cli:unknown_failure', 'component_install:started:skills:', 'component_install:succeeded:skills:', 'component_install:failed:skills:unknown_failure') -cnotcontains "${Type}:${Result}:${Component}:${Reason}") { return }
        $runtime = (Get-InvokingHost).ToLowerInvariant()
        $homeDir = Resolve-EigenfluxHome -ExplicitHome $ExplicitEigenfluxHome -InvokingHost $runtime
        if (-not [IO.Path]::IsPathRooted($homeDir)) { return }
        foreach ($name in @('credentials.json', 'agent-v2-credentials.json')) {
            if (Test-Path -LiteralPath (Join-Path $homeDir $name)) { return }
        }
        $servers = Join-Path $homeDir 'servers'
        if (Test-Path -LiteralPath $servers) {
            if ((Get-Item -LiteralPath $servers -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { return }
            foreach ($server in @(Get-ChildItem -LiteralPath $servers -Directory -Force)) {
                # Do not read credentials, including credentials belonging to another host.
                if ($server.Attributes -band [IO.FileAttributes]::ReparsePoint) { return }
                foreach ($name in @('credentials.json', 'agent-v2-credentials.json')) {
                    if (Test-Path -LiteralPath (Join-Path $server.FullName $name)) { return }
                }
            }
        }
        $origin = $Endpoint.TrimEnd('/')
        if ($origin -cnotmatch '^https?://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/-]+)?$') { return }
        $utf8 = New-Object Text.UTF8Encoding($false)
        $sha = [Security.Cryptography.SHA256]::Create()
        try { $digest = ([BitConverter]::ToString($sha.ComputeHash($utf8.GetBytes($origin)))).Replace('-', '').ToLowerInvariant() } finally { $sha.Dispose() }
        $base = Join-Path $homeDir 'install-observations'
        $directory = Join-Path $base $digest
        Protect-ObservationDirectory $base
        Protect-ObservationDirectory $directory
        $now = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
        $path = Join-Path $directory 'session.json'
        if (-not (Test-Path -LiteralPath $path)) {
            if ($Type -cne 'install_started') { return }
            $newSession = [ordered]@{ install_session_id = (New-ObservationHex 16); proof = (New-ObservationHex 32); endpoint = $origin; created_at = $now }
            $temporary = Join-Path $directory ('.installer-' + (New-ObservationHex 16))
            [IO.File]::WriteAllText($temporary, ($newSession | ConvertTo-Json -Compress), $utf8)
            try { [IO.File]::Move($temporary, $path) } catch { if (-not (Test-Path -LiteralPath $path)) { throw } }
            Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
            $temporary = $null
        }
        $info = Get-Item -LiteralPath $path -Force
        if ($info.PSIsContainer -or ($info.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $info.Length -gt 4096) { return }
        $raw = [IO.File]::ReadAllText($path)
        $session = $raw | ConvertFrom-Json
        $keys = @($session.PSObject.Properties.Name)
        if ($keys.Count -ne 4 -or @($keys | Where-Object { @('install_session_id','proof','endpoint','created_at') -cnotcontains $_ }).Count -ne 0) { return }
        if ($session.install_session_id -cnotmatch '^[a-f0-9]{32}$' -or $session.proof -cnotmatch '^[a-f0-9]{64}$' -or $session.endpoint -cne $origin -or $session.created_at -lt ($now - 604800000) -or $session.created_at -gt ($now + 60000)) { return }
        $queue = Join-Path $directory $session.install_session_id
        Protect-ObservationDirectory $queue
        if (@(Get-ChildItem -LiteralPath $queue -Force).Count -ge 128) { return }
        if (@('codex','openclaw','workbuddy','hermes','deepseek','deepseek-harness','dsh','claude-code','claude','cursor','opencode','gemini-cli','gemini','qoder','trae','kilo','roo','cline','windsurf','antigravity','kimi','kiro','jarvis') -cnotcontains $runtime) { $runtime = 'unknown' }
        if ($Type -eq 'host_detected' -and $runtime -eq 'unknown') { $Result = 'pending' }
        $event = [ordered]@{ event_id = (New-ObservationHex 16); event_at = $now; event_type = $Type; result = $Result; component = $Component; runtime_name = $runtime; reason = $Reason }
        $temporary = Join-Path $queue ('.installer-' + (New-ObservationHex 16))
        [IO.File]::WriteAllText($temporary, ($event | ConvertTo-Json -Compress), $utf8)
        if ([IO.File]::ReadAllText($path) -cne $raw) { return }
        [IO.File]::Move($temporary, (Join-Path $queue ($event.event_id + '.json')))
        $temporary = $null
        $payload = @{ install_session_id = $session.install_session_id; events = @($event) } | ConvertTo-Json -Compress -Depth 4
        # No redirects, credential output or acknowledgement-based deletion. The
        # CLI will verify the batch receipt and drain these immutable retries.
        Invoke-WebRequest -Uri ($origin + '/api/v2/install-telemetry/events:batch') -Method Post -ContentType 'application/json' -Body $payload -Headers @{ 'X-Install-Session-Proof' = $session.proof } -MaximumRedirection 0 -TimeoutSec 2 -UseBasicParsing | Out-Null
    } catch {
        # Missing evidence must not change installation exit codes or diagnostics.
    } finally {
        if ($temporary) { Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue }
    }
}

# ── Helper: download with retry, temp file, optional SHA256 ──

function Download-WithRetry {
    param(
        [string]$Url,
        [string]$Destination,
        [ValidateRange(1, 10)][int]$MaxRetries = 3,
        [string]$Sha256Url = "",
        [switch]$CliInstallation
    )

    $tmpFile = Join-Path $env:TEMP ("eigenflux-dl-" + [System.IO.Path]::GetRandomFileName() + ".exe")
    $attempt = 0
    $downloaded = $false

    try {
        while ($attempt -lt $MaxRetries -and -not $downloaded) {
            $attempt++
            try {
                if ($attempt -gt 1) { Info "Retry ${attempt}/${MaxRetries}..." }
                Invoke-WebRequest -Uri $Url -OutFile $tmpFile -UseBasicParsing -TimeoutSec 30
                $downloaded = $true
            } catch {
                if ($attempt -ge $MaxRetries) {
                    throw "Download failed after ${MaxRetries} attempts: ${Url}`n$($_.Exception.Message)"
                }
                Remove-Item -Force $tmpFile -ErrorAction SilentlyContinue
                Start-Sleep -Seconds (2 * $attempt)
            }
        }

        if ($Sha256Url) {
            # A missing checksum is a failed release, not permission to install
            # unverified bytes. Use text explicitly on both PowerShell 5.1 and 7.
            try {
                $response = Invoke-WebRequest -Uri $Sha256Url -UseBasicParsing -TimeoutSec 30
            } catch {
                throw "Failed to fetch SHA256 checksum from ${Sha256Url}: $($_.Exception.Message)"
            }
            $checksum = $response.Content
            if ($checksum -is [byte[]]) { $checksum = [System.Text.Encoding]::UTF8.GetString($checksum) }
            $expectedHash = ([string]$checksum).Trim()
            if ($expectedHash -notmatch '^[a-fA-F0-9]{64}$') {
                throw "Invalid SHA256 checksum from ${Sha256Url}"
            }
            $actualHash = (Get-FileHash -Path $tmpFile -Algorithm SHA256).Hash
            if ($actualHash -ne $expectedHash) {
                throw "SHA256 mismatch for ${Url}: expected ${expectedHash}, got ${actualHash}"
            }
            Ok "SHA256 verified"
        }

        $destDir = Split-Path -Parent $Destination
        if (-not (Test-Path $destDir)) {
            New-Item -ItemType Directory -Path $destDir -Force | Out-Null
        }
        if ($CliInstallation) {
            $hostName = Get-InvokingHost
            $homeDir = Resolve-EigenfluxHome -ExplicitHome $ExplicitEigenfluxHome -InvokingHost $hostName
            & $tmpFile --homedir $homeDir installation replace "--destination=$Destination" "--host=$hostName" "--skills-target="
            if ($LASTEXITCODE -ne 0) { throw "CLI installation failed; another installation or uninstall may be active." }
        } else {
            Move-Item -Path $tmpFile -Destination $Destination -Force
        }
    } finally {
        Remove-Item -Force $tmpFile -ErrorAction SilentlyContinue
    }
}

# Only HTTP 404 permits fallback to the legacy bridge release channel.
function Get-LatestCliVersion {
    try {
        $value = (Invoke-RestMethod -Uri "${CdnUrl}/cli/v2/latest/version.txt" -TimeoutSec 30).Trim()
    } catch {
        if ($null -eq $_.Exception.Response -or [int]$_.Exception.Response.StatusCode -ne 404) {
            throw
        }
        $value = (Invoke-RestMethod -Uri "${CdnUrl}/cli/latest/version.txt" -TimeoutSec 30).Trim()
    }
    if ($value -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw 'Invalid published CLI version'
    }
    return $value
}

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
        $script:latestVersion = Get-LatestCliVersion
    } catch {
        Err "Failed to fetch latest version from ${CdnUrl}"
    }
    Info "Latest version: ${script:latestVersion}"

    $currentVersion = $null
    $eigenfluxCmd = Get-Command eigenflux -ErrorAction SilentlyContinue
    if ($eigenfluxCmd) {
        try { $currentVersion = (& eigenflux version --short 2>$null).Trim() } catch {}
        if ($currentVersion -eq $script:latestVersion) {
            $script:installedCliPath = $eigenfluxCmd.Source
            $script:installDir = Split-Path -Parent $script:installedCliPath
            Ok "eigenflux ${currentVersion} is already up to date."
            return
        }
        Info "Upgrading eigenflux ${currentVersion} -> ${script:latestVersion}"
    } else {
        Info "Installing eigenflux ${script:latestVersion}"
    }

    $downloadUrl = "${CdnUrl}/cli/${script:latestVersion}/${binName}"
    $sha256Url = "${CdnUrl}/cli/${script:latestVersion}/${binName}.sha256"

    # Resolve install directory. Priority:
    #   1. EIGENFLUX_INSTALL_DIR env var (explicit user override)
    #   2. D:\eigenflux when a D: drive exists (default)
    #   3. %LOCALAPPDATA%\local\bin (fallback for C-only machines)
    $fallbackDir = Join-Path $env:LOCALAPPDATA "local\bin"
    if ($env:EIGENFLUX_INSTALL_DIR) {
        $script:installDir = $env:EIGENFLUX_INSTALL_DIR
        Info "Using EIGENFLUX_INSTALL_DIR: $($script:installDir)"
    } elseif (Test-Path "D:\") {
        $script:installDir = "D:\eigenflux"
    } else {
        $script:installDir = $fallbackDir
        Info "D: drive not found; installing to ${fallbackDir}"
    }
    $installPath = Join-Path $script:installDir "eigenflux.exe"
    $script:installedCliPath = $installPath

    Info "Downloading ${downloadUrl}..."
    Download-WithRetry -Url $downloadUrl -Destination $installPath -Sha256Url $sha256Url -CliInstallation

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$($script:installDir)*") {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$($script:installDir)", "User")
        $env:Path = "$env:Path;$($script:installDir)"
        Info "Added $($script:installDir) to user PATH"
    }

    $script:installedCliPath = $installPath
    Ok "eigenflux ${script:latestVersion} installed successfully"
    try { & $installPath version } catch {}
}

function Save-InstallReferral {
    if (-not $Ref) { return }
    $homeDir = Resolve-EigenfluxHome -ExplicitHome $ExplicitEigenfluxHome -InvokingHost (Get-InvokingHost)
    & $script:installedCliPath --homedir $homeDir agent install-ref --ref $Ref --endpoint $Endpoint | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Referral code could not be saved for this Agent Home and server; resolve the error and retry before onboarding."
    }
}

function Report-InstallAttribution {
    if (-not $Ref) { return }
    $payload = @{ ref = $Ref; metadata = @{ os = 'windows'; via = 'install.ps1' } } | ConvertTo-Json -Compress
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        $status = 0
        try {
            $response = Invoke-WebRequest -Uri "${Endpoint}/api/v1/install/report" -Method Post -ContentType 'application/json' -Body $payload -UseBasicParsing -TimeoutSec 15
            $status = [int]$response.StatusCode
            $body = $response.Content
            if ($body -is [byte[]]) { $body = [System.Text.Encoding]::UTF8.GetString($body) }
            $receipt = [string]$body | ConvertFrom-Json
            if ($status -eq 200 -and ($receipt.code -is [int] -or $receipt.code -is [long]) -and $receipt.code -eq 0 -and
                $receipt.data.converted -is [bool] -and $receipt.data.attribution.ref -ceq $Ref) {
                Ok "Install attributed (ref ${Ref})"
                return
            }
        } catch {
            if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
        }
        if ($status -ne 0 -and $status -ne 200 -and $status -ne 408 -and $status -ne 429 -and $status -lt 500) { break }
        if ($attempt -lt 3) { Start-Sleep -Seconds $attempt }
    }
    Info "CLI installation succeeded, but attribution reporting is incomplete (HTTP ${status}); installation continues."
}

# ── Step 2: Install skills ────────────────────────────────────

function Install-Skills {
    $hostName = Get-InvokingHost
    $homeDir = Resolve-EigenfluxHome -ExplicitHome $ExplicitEigenfluxHome -InvokingHost $hostName
    $cli = $script:installedCliPath
    $script:installedSkillsTarget = ""
    Info "Installing EigenFlux skills..."
    & $cli --homedir $homeDir skills sync "--host=$hostName"
    if ($LASTEXITCODE -ne 0) { throw "Skills sync failed; retry installation before onboarding." }
    $script:installedSkillsTarget = (& $cli --homedir $homeDir skills path "--host=$hostName" | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or -not $script:installedSkillsTarget) { throw "Could not resolve the managed Skills directory." }
    Ok "EigenFlux skills synced to $($script:installedSkillsTarget)"
}

# ── Step 3: Migrate legacy config ─────────────────────────────

function Migrate-Config {
    $installPath = $script:installedCliPath
    $openclawStateDir = Join-Path $env:USERPROFILE ".openclaw"
    $invokingHost = Get-InvokingHost
    $efHome = Resolve-EigenfluxHome -ExplicitHome $ExplicitEigenfluxHome -InvokingHost $invokingHost
    $env:EIGENFLUX_HOME = $efHome

    if ($invokingHost -eq "openclaw") {
        if (-not (Test-Path $openclawStateDir)) {
            New-Item -ItemType Directory -Path $openclawStateDir -Force | Out-Null
        }
        $envFile = Join-Path $openclawStateDir ".env"
        $envLine = "EIGENFLUX_HOME=`"${efHome}`""

        if (-not (Test-Path $envFile)) {
            New-Item -ItemType File -Path $envFile -Force | Out-Null
        }
        $existing = @(Get-Content $envFile -ErrorAction SilentlyContinue | Where-Object { $_ -notmatch '^EIGENFLUX_HOME=' })
        Set-Content -Path $envFile -Value @($existing + $envLine)
        Info "Set EIGENFLUX_HOME in ${envFile}"
    }

    try { & $installPath --homedir $efHome migrate 2>$null } catch {}
}

# ── Step 4: Detect and configure AI agents ────────────────────

# Keep these boundaries aligned with the published plugins' minGatewayVersion
# and static/install.sh: current 2026.5.7, pinned 0.0.8 2026.3.1.
function Select-OpenClawPlugin {
    $ocVersion = $null
    if ($env:OPENCLAW_VERSION) {
        $ocVersion = $env:OPENCLAW_VERSION
        Info "Using OPENCLAW_VERSION from environment: ${ocVersion}"
    } else {
        try {
            $ocRaw = & openclaw --version 2>&1 | Out-String
            if ($LASTEXITCODE -eq 0) {
                $versionMatch = [regex]::Match($ocRaw, '(?m)(?<!\S)v?([0-9]+\.[0-9]+\.[0-9]+\S*)')
                if ($versionMatch.Success) { $ocVersion = $versionMatch.Groups[1].Value }
            }
        } catch {
            $ocVersion = $null
        }
    }
    $parsedVersion = $null
    if (-not $ocVersion -or $ocVersion -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' -or
        -not [version]::TryParse($ocVersion, [ref]$parsedVersion)) {
        Write-Warning "Could not verify a stable OpenClaw version; plugin setup skipped. Check openclaw --version, upgrade to 2026.5.7 or later, then retry."
        Write-Host '{"component":"openclaw-eigenflux","status":"skipped","reason":"unverified_gateway_version"}'
        return $null
    }
    if ($parsedVersion -ge [version]'2026.5.7') {
        $pluginSpec = "@phronesis-io/openclaw-eigenflux"
    } elseif ($parsedVersion -ge [version]'2026.3.1' -and $parsedVersion -lt [version]'2026.5.2') {
        $pluginSpec = "@phronesis-io/openclaw-eigenflux@0.0.8"
    } else {
        Write-Warning "OpenClaw ${ocVersion} has no verified compatible plugin selection; plugin setup skipped. Upgrade OpenClaw to 2026.5.7 or later, then retry."
        Write-Host '{"component":"openclaw-eigenflux","status":"skipped","reason":"unsupported_gateway_version"}'
        return $null
    }
    return [pscustomobject]@{ Version = $ocVersion; Spec = $pluginSpec }
}

function Setup-Agents {
    $openclawCmd = Get-Command openclaw -ErrorAction SilentlyContinue
    if (-not $openclawCmd) { return }

    Info ""
    Info "OpenClaw environment detected."
    $selection = Select-OpenClawPlugin
    if (-not $selection) { return }
    $pluginSpec = $selection.Spec
    Info "OpenClaw version: $($selection.Version) -> plugin: ${pluginSpec}"

    function Install-OpenClawPlugin {
        param(
            [string]$Spec,
            [bool]$AlreadyInstalled
        )

        if ($AlreadyInstalled -and $Spec -ne "@phronesis-io/openclaw-eigenflux") {
            Info "Reinstalling OpenClaw plugin with ${Spec}..."
            try { & openclaw plugins uninstall openclaw-eigenflux --force 2>$null } catch {}
            & openclaw plugins install $Spec
        } elseif ($AlreadyInstalled) {
            Info "Updating OpenClaw plugin to latest..."
            & openclaw plugins update openclaw-eigenflux 2>$null
            if ($LASTEXITCODE -ne 0) {
                & openclaw plugins install $Spec
            }
        } else {
            & openclaw plugins install $Spec
        }
    }

    $pluginInstalled = $false
    try {
        if ((& openclaw plugins list 2>$null) -match "eigenflux") {
            $pluginInstalled = $true
        }
    } catch {}

    $pluginChanged = $false
    if (-not $pluginInstalled) {
        if ([Console]::IsOutputRedirected) {
            Info "Non-interactive shell; installing openclaw-eigenflux plugin automatically..."
            Install-OpenClawPlugin -Spec $pluginSpec -AlreadyInstalled $pluginInstalled
            Ok "OpenClaw plugin installed"
            $pluginChanged = $true
        } else {
            $reply = Read-Host "OpenClaw detected. Install the openclaw-eigenflux plugin automatically? [Y/n]"
            if ($reply -match '^[nN]') {
                Info "Skipped OpenClaw plugin installation"
            } else {
                Info "Installing ${pluginSpec}..."
                Install-OpenClawPlugin -Spec $pluginSpec -AlreadyInstalled $pluginInstalled
                Ok "OpenClaw plugin installed"
                $pluginChanged = $true
            }
        }
    } else {
        Install-OpenClawPlugin -Spec $pluginSpec -AlreadyInstalled $pluginInstalled
        Ok "OpenClaw plugin aligned to ${pluginSpec}"
        $pluginChanged = $true
    }

    if ($pluginChanged -and $env:EIGENFLUX_ALLOW_HOST_RESTART -eq "1") {
        Info "Restarting OpenClaw gateway..."
        try {
            & openclaw gateway restart 2>$null
            Ok "OpenClaw gateway restarted"
        } catch {
            Info "OpenClaw gateway restart failed; run 'openclaw gateway restart' manually"
        }
    }
}

function Setup-Notifications {
    $cli = $script:installedCliPath
    $homeDir = Resolve-EigenfluxHome -ExplicitHome $ExplicitEigenfluxHome -InvokingHost (Get-InvokingHost)
    Info "Registering Windows notifications without sending a test. Windows does not display an Allow permission dialog."
    & $cli --homedir $homeDir notify setup --source installer
    if ($LASTEXITCODE -ne 0) {
        throw "CLI is installed, but notification setup failed. Retry: & '$cli' --homedir '$homeDir' notify setup. If disabled, check Settings > System > Notifications."
    }
    & $cli --homedir $homeDir notify status
    if ($LASTEXITCODE -ne 0) { throw "CLI is installed, but notification status is not ready. Run notify status to inspect the error." }
    Ok "Windows notification registration is complete. Before the first real notification, status may be registered with an unknown OS setting. Use notify log for local observations."
}

# ── Main ──────────────────────────────────────────────────────

Write-InstallObservation install_started started
Write-InstallObservation host_detected succeeded
$script:observationComponent = 'cli'
Write-InstallObservation component_install started cli
try { Install-Cli } catch {
    Write-InstallObservation component_install failed cli unknown_failure
    throw
}
Write-InstallObservation component_install succeeded cli
$script:observationComponent = ''
Migrate-Config
Save-InstallReferral
Report-InstallAttribution
$script:observationComponent = 'skills'
Write-InstallObservation component_install started skills
try { Install-Skills } catch {
    Write-InstallObservation component_install failed skills unknown_failure
    throw
}
Write-InstallObservation component_install succeeded skills
$script:observationComponent = ''
Setup-Agents
$recordBin = $script:installedCliPath
$recordHost = Get-InvokingHost
& $recordBin --homedir $env:EIGENFLUX_HOME installation record "--host=$recordHost" "--skills-target=$($script:installedSkillsTarget)" | Out-Null
if ($LASTEXITCODE -ne 0) { Info "Installation registration unavailable; record it with the new CLI before uninstalling." }
# Notification permission is independent of background order processing. Preserve
# the diagnostic, but always attempt outer-loop recovery even when setup fails.
$notificationSetupError = $null
try {
    Setup-Notifications
} catch {
    $notificationSetupError = $_.Exception.Message
    Info "Notifications need attention: $notificationSetupError"
}
$watchHome = Resolve-EigenfluxHome -ExplicitHome $ExplicitEigenfluxHome -InvokingHost (Get-InvokingHost)
if ($env:EIGENFLUX_SKIP_AGENT_SETUP -ne '1') {
    & $script:installedCliPath watch recover --help *> $null
    if ($LASTEXITCODE -eq 0) {
        & $script:installedCliPath --homedir $watchHome watch recover
        if ($LASTEXITCODE -ne 0) { throw "CLI is installed, but outer-loop recovery is incomplete. Retry watch recover with the same Home." }
    } else {
        & $script:installedCliPath --homedir $watchHome watch start
        if ($LASTEXITCODE -ne 0) { throw "CLI is installed, but notification watch startup failed." }
    }
}
Info "Updated plugin files may require a host restart after current work finishes."

if ($notificationSetupError) {
    Info "CLI installation completed; notifications are not ready. $notificationSetupError"
}

Ok ""
if ([Console]::IsOutputRedirected) {
    Ok "Done! Check ef-onboarding skill to start Console V2 onboarding"
} else {
    Ok 'Done! Send this to your agents "Read ef-onboarding skill to help me join EigenFlux"'
}
