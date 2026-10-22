param([Parameter(Mandatory=$true)][string]$RequestFile)
$ErrorActionPreference = 'Stop'
# Keep the native child in a kill-on-close job. Task Scheduler stopping the
# PowerShell wrapper must also stop watch and release its account lock.
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
public static class EigenFluxWatchJob {
    [StructLayout(LayoutKind.Sequential)] struct Basic {
        public long ProcessTime, JobTime;
        public uint Flags;
        public UIntPtr MinWorkingSet, MaxWorkingSet;
        public uint ActiveProcesses;
        public UIntPtr Affinity;
        public uint Priority, Scheduling;
    }
    [StructLayout(LayoutKind.Sequential)] struct Counters {
        public ulong ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes;
    }
    [StructLayout(LayoutKind.Sequential)] struct Limits {
        public Basic Basic;
        public Counters IO;
        public UIntPtr ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory;
    }
    [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr attributes, string name);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job, int kind, ref Limits limits, uint size);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
    static IntPtr ownedJob;
    public static void Attach() {
        IntPtr job = CreateJobObject(IntPtr.Zero, null);
        if (job == IntPtr.Zero) throw new Win32Exception();
        Limits limits = new Limits();
        limits.Basic.Flags = 0x2000;
        if (!SetInformationJobObject(job, 9, ref limits, (uint)Marshal.SizeOf(typeof(Limits))) ||
            !AssignProcessToJobObject(job, Process.GetCurrentProcess().Handle)) {
            int error = Marshal.GetLastWin32Error();
            CloseHandle(job);
            throw new Win32Exception(error);
        }
        ownedJob = job;
    }
}
'@
[EigenFluxWatchJob]::Attach()
$request = Get-Content -LiteralPath $RequestFile -Raw -Encoding UTF8 | ConvertFrom-Json
Get-ChildItem Env: | Where-Object { $_.Name -like 'EIGENFLUX_*' } | ForEach-Object { Remove-Item -LiteralPath ('Env:' + $_.Name) }
$env:EIGENFLUX_WATCH_EXPECTED_AGENT = [string]$request.agent_id
$env:EIGENFLUX_WATCH_EXPECTED_PRINCIPAL = [string]$request.principal_id
$log = [string]$request.log
if ((Test-Path -LiteralPath $log) -and (Get-Item -LiteralPath $log).Length -gt 4MB) {
    Move-Item -LiteralPath $log -Destination ($log + '.previous') -Force
}
# A bounded host supervisor restarts failed children without waiting for logon.
# Clear only routing overrides above; keep the pinned identity for every restart.
$watchArgs = @('--homedir', [string]$request.home, '--server', [string]$request.server, 'watch')
if ($request.dispatch) { $watchArgs += '--dispatch' } else { $watchArgs += '--notifications-only' }
$watchArgs += @('--format', 'json')
for ($attempt = 0; $attempt -lt 4; $attempt++) {
    $ErrorActionPreference = 'Continue'
    [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
    try {
        & ([string]$request.cli) @watchArgs 2>&1 | ForEach-Object {
            [IO.File]::AppendAllText($log, ([string]$_ + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
        }
        $exitCode = $LASTEXITCODE
    } catch { $exitCode = 127 }
    $ErrorActionPreference = 'Stop'
    [IO.File]::AppendAllText($log, ((@{type='watch_process_exit';data=@{exit_code=$exitCode;attempt=$attempt;dispatch=[bool]$request.dispatch};time=[DateTime]::UtcNow.ToString('o')} | ConvertTo-Json -Compress -Depth 4) + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
    if ($exitCode -eq 0 -or $exitCode -eq 78) { exit $exitCode }
    if ($attempt -lt 3) { Start-Sleep -Seconds 15 }
}
exit $exitCode
