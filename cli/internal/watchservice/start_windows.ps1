$ErrorActionPreference = 'Stop'
$failureCode = 'scheduler_access'
function Write-StartResult($value) {
    $bytes = [Text.Encoding]::UTF8.GetBytes(($value | ConvertTo-Json -Compress))
    $stream = [Console]::OpenStandardOutput()
    $stream.Write($bytes, 0, $bytes.Length)
    $stream.Flush()
}
try {
    $reader = [IO.StreamReader]::new([Console]::OpenStandardInput(), [Text.Encoding]::UTF8)
    $payload = $reader.ReadToEnd() | ConvertFrom-Json
    $request = $payload.request
    $service = New-Object -ComObject 'Schedule.Service'
    $service.Connect()
    $folder = $service.GetFolder('\')
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $name = [string]$request.task_name
    $description = 'EigenFlux notification watch ' + [string]$request.scope
    $existing = $null
    try { $existing = $folder.GetTask($name) } catch {
        if ($_.Exception.GetBaseException().HResult -ne -2147024894) { throw }
    }
    if ($null -ne $existing) {
        $failureCode = 'task_ownership'
        $principal = $existing.Definition.Principal
        $taskSID = [string]$principal.UserId
        if ($taskSID -notmatch '^S-1-') { $taskSID = ([Security.Principal.NTAccount]::new($taskSID)).Translate([Security.Principal.SecurityIdentifier]).Value }
        if ($existing.Definition.RegistrationInfo.Description -ne $description -or $taskSID -ne $sid -or $principal.LogonType -ne 3 -or $principal.RunLevel -ne 0) { throw 'different task owner' }
        $failureCode = 'task_disabled'
        if (-not $existing.Enabled) { throw 'preserve disabled task' }
        # Reuse only an identical launcher. Upgrades replace this owned task,
        # not unrelated host tasks, and wait for the old Job Object to close.
        $sameLauncher = $false
        if (-not $request.prepare_only -and (Test-Path -LiteralPath $request.request_file) -and (Test-Path -LiteralPath $request.wrapper)) {
            $old = Get-Content -LiteralPath $request.request_file -Raw -Encoding UTF8 | ConvertFrom-Json
            $sameLauncher = ($old.cli -eq $request.cli -and $old.agent_id -eq $request.agent_id -and $old.principal_id -eq $request.principal_id -and $old.home -eq $request.home -and $old.server -eq $request.server -and [bool]$old.dispatch -eq [bool]$request.dispatch -and (Get-Content -LiteralPath $request.wrapper -Raw -Encoding UTF8) -eq $payload.wrapper_content)
        }
        if ($existing.State -eq 4 -and $sameLauncher) {
            Write-StartResult @{status='process_started'}
            exit 0
        }
        if ($existing.State -eq 4) {
            $failureCode = 'stop_failed'
            $existing.Stop(0)
            $deadline = [DateTime]::UtcNow.AddSeconds(10)
            while ($existing.GetInstances(0).Count -gt 0) {
                if ([DateTime]::UtcNow -ge $deadline) { throw 'stop timeout' }
                Start-Sleep -Milliseconds 200
            }
            Start-Sleep -Seconds 1
        }
    }
    if ($request.prepare_only) {
        Write-StartResult @{status='task_stopped'}
        exit 0
    }
    $failureCode = 'launcher_write'
    $utf8 = [Text.UTF8Encoding]::new($false)
    [IO.File]::WriteAllText([string]$request.request_file, ($request | ConvertTo-Json -Compress), $utf8)
    [IO.File]::WriteAllText([string]$request.wrapper, [string]$payload.wrapper_content, $utf8)
    $definition = $service.NewTask(0)
    $definition.RegistrationInfo.Description = $description
    $definition.Principal.UserId = $sid
    $definition.Principal.LogonType = 3
    $definition.Principal.RunLevel = 0
    $definition.Settings.Enabled = $true
    $definition.Settings.Hidden = $false
    $definition.Settings.AllowDemandStart = $true
    $definition.Settings.DisallowStartIfOnBatteries = $false
    $definition.Settings.StopIfGoingOnBatteries = $false
    $definition.Settings.ExecutionTimeLimit = 'PT0S'
    $definition.Settings.RunOnlyIfIdle = $false
    $definition.Settings.IdleSettings.StopOnIdleEnd = $false
    $definition.Settings.StartWhenAvailable = $true
    $definition.Settings.MultipleInstances = 2
    $definition.Settings.RestartInterval = 'PT1M'
    $definition.Settings.RestartCount = 3
    $trigger = $definition.Triggers.Create(9)
    $trigger.UserId = $sid
    $action = $definition.Actions.Create(0)
    $action.Path = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $action.Arguments = '-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + [string]$request.wrapper + '" -RequestFile "' + [string]$request.request_file + '"'
    $action.WorkingDirectory = [string]$request.home
    $failureCode = 'task_register'
    $task = $folder.RegisterTaskDefinition($name, $definition, 6, $sid, $null, 3, $null)
    $failureCode = 'start_failed'
    $instance = $task.Run($null)
    if ([object]::ReferenceEquals($null, $instance)) { throw 'no instance' }
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    $stableSince = $null
    do {
        Start-Sleep -Milliseconds 500
        try { $instance.Refresh() } catch {
            if ($null -eq $stableSince -and $_.Exception.GetBaseException().HResult -eq -2147216629) { continue }
            throw
        }
        if ($instance.State -eq 4) {
            if ($null -eq $stableSince) { $stableSince = [DateTime]::UtcNow }
            if (([DateTime]::UtcNow - $stableSince).TotalSeconds -ge 3) {
                Write-StartResult @{status='process_started'}
                exit 0
            }
        } elseif ($instance.State -eq 2) {
            $stableSince = $null
        } else { throw 'process exited' }
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'start timeout'
} catch {
    Write-StartResult @{status='failed';error=$failureCode}
    exit 1
}
