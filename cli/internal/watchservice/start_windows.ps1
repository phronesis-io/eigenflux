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
        if ($existing.Definition.RegistrationInfo.Description -ne $description -or $principal.UserId -ne $sid -or $principal.LogonType -ne 3 -or $principal.RunLevel -ne 0) { throw 'different task owner' }
        $failureCode = 'task_disabled'
        if (-not $existing.Enabled) { throw 'preserve disabled task' }
        # Repeated installation must not kill an active consumer or duplicate it.
        if ($existing.State -eq 4) {
            Write-StartResult @{status='process_started'}
            exit 0
        }
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
    $definition.Settings.Hidden = $true
    $definition.Settings.AllowDemandStart = $true
    $definition.Settings.DisallowStartIfOnBatteries = $false
    $definition.Settings.StopIfGoingOnBatteries = $false
    $definition.Settings.ExecutionTimeLimit = 'PT0S'
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
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        Start-Sleep -Milliseconds 500
        $instance.Refresh()
        if ($instance.State -eq 4) {
            Start-Sleep -Seconds 3
            $instance.Refresh()
            if ($instance.State -ne 4) { throw 'process exited' }
            Write-StartResult @{status='process_started'}
            exit 0
        }
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'start timeout'
} catch {
    Write-StartResult @{status='failed';error=$failureCode}
    exit 1
}
