$ErrorActionPreference = 'Stop'
$failureCode = 'scheduler_access'
function Write-RestartResult($value) {
    # Direct UTF-8 output also preserves non-ASCII task paths without a console.
    $bytes = [Text.Encoding]::UTF8.GetBytes(($value | ConvertTo-Json -Compress))
    $stream = [Console]::OpenStandardOutput()
    $stream.Write($bytes, 0, $bytes.Length)
    $stream.Flush()
}
try {
    # Data stays in stdin; neither the task name nor its stored actions become script text.
    $reader = [IO.StreamReader]::new([Console]::OpenStandardInput(), [Text.Encoding]::UTF8)
    $request = $reader.ReadToEnd() | ConvertFrom-Json
    $failureCode = 'invalid_task_name'
    $taskName = [string]$request.task_name
    if ([string]::IsNullOrWhiteSpace($taskName) -or $taskName -match '[\x00-\x1f/*?:]') { throw 'invalid name' }
    if (-not $taskName.StartsWith('\')) { $taskName = '\' + $taskName }
    foreach ($part in $taskName.Substring(1).Split('\')) {
        if ($part -eq '' -or $part -eq '.' -or $part -eq '..') { throw 'invalid path' }
    }

    $failureCode = 'scheduler_access'
    $service = New-Object -ComObject 'Schedule.Service'
    $service.Connect()
    $folder = $service.GetFolder('\')
    $failureCode = 'task_not_found'
    # GetTask is an exact COM lookup, not a PowerShell wildcard query.
    $task = $folder.GetTask($taskName)
    if (-not [string]::Equals([string]$task.Path, $taskName, [StringComparison]::OrdinalIgnoreCase)) { throw 'path mismatch' }

    $failureCode = 'task_identity'
    $principal = $task.Definition.Principal
    $currentSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $userID = [string]$principal.UserId
    if ([string]::IsNullOrWhiteSpace($userID)) { throw 'missing user' }
    if ($userID -match '^S-1-') {
        $taskSID = ([Security.Principal.SecurityIdentifier]::new($userID)).Value
    } else {
        $taskSID = ([Security.Principal.NTAccount]::new($userID)).Translate([Security.Principal.SecurityIdentifier]).Value
    }
    if ($taskSID -in @('S-1-5-18', 'S-1-5-19', 'S-1-5-20') -or $taskSID -ne $currentSID) { throw 'different user or system account' }
    $failureCode = 'interactive_required'
    if ([int]$principal.LogonType -ne 3) { throw 'interactive token required' }
    $failureCode = 'elevation_required'
    if ([int]$principal.RunLevel -ne 0) { throw 'elevated task' }
    $failureCode = 'task_disabled'
    if (-not $task.Enabled) { throw 'disabled' }
    $failureCode = 'demand_start_disabled'
    if (-not $task.Definition.Settings.AllowDemandStart) { throw 'manual start disabled' }

    $failureCode = 'stop_failed'
    if ($task.GetInstances(0).Count -gt 0) { $task.Stop(0) }
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    while ($task.GetInstances(0).Count -gt 0) {
        if ([DateTime]::UtcNow -ge $deadline) { throw 'stop timeout' }
        Start-Sleep -Milliseconds 200
    }

    $failureCode = 'start_failed'
    # Preserve the registered action, arguments, environment and working directory.
    $instance = $task.Run($null)
    # PowerShell equality can query a COM default property before the async
    # instance exists, raising SCHED_E_TASK_NOT_RUNNING on a valid Run request.
    if ([object]::ReferenceEquals($null, $instance)) { throw 'no running instance' }
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    $stableSince = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 250
        $instance.Refresh()
        $state = [int]$instance.State
        if ($state -eq 4) {
            if ($null -eq $stableSince) { $stableSince = [DateTime]::UtcNow }
            if (([DateTime]::UtcNow - $stableSince).TotalSeconds -ge 3) {
                Write-RestartResult @{ status = 'process_started'; target = [string]$task.Path }
                exit 0
            }
        } elseif ($state -eq 2) {
            $stableSince = $null
        } else {
            $failureCode = 'process_exited'
            throw 'instance stopped'
        }
        Start-Sleep -Milliseconds 250
    }
    $failureCode = 'start_timeout'
    throw 'start timeout'
} catch {
    Write-RestartResult @{ error = $failureCode }
    exit 1
}
