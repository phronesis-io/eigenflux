param(
  [Parameter(Mandatory=$true)][string]$CliPath,
  [Parameter(ValueFromRemainingArguments=$true)][string[]]$UninstallArgs
)
$ErrorActionPreference = 'Stop'
function ConvertTo-NativeArgument([string]$value) {
  # Windows argv quoting; reason text and paths remain argument data.
  return '"' + [regex]::Replace([regex]::Replace($value, '(\\*)"', '$1$1\"'), '(\\+)$', '$1$1') + '"'
}
function Invoke-EigenFluxJson([string]$binary, [string[]]$cliArgs) {
  $info = New-Object Diagnostics.ProcessStartInfo
  $info.FileName = $binary
  $info.Arguments = ($cliArgs | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
  $info.UseShellExecute = $false
  $info.CreateNoWindow = $true
  $info.RedirectStandardOutput = $true
  $info.StandardOutputEncoding = New-Object Text.UTF8Encoding($false)
  $process = New-Object Diagnostics.Process
  $process.StartInfo = $info
  try {
    [void]$process.Start()
    $output = $process.StandardOutput.ReadToEnd()
    $process.WaitForExit()
    return [PSCustomObject]@{ ExitCode = $process.ExitCode; Output = $output }
  } finally { $process.Dispose() }
}
$resolved = (Resolve-Path -LiteralPath $CliPath).Path
$call = Invoke-EigenFluxJson $resolved (@('uninstall') + @($UninstallArgs) + @('--script-protocol=1', '--format', 'json'))
if ($call.ExitCode -ne 0) { exit $call.ExitCode }
$result = $call.Output | ConvertFrom-Json
if (-not ($result.apply -and $result.executable_removal_pending)) {
  $result | ConvertTo-Json -Depth 10
  exit 0
}
$temporary = $null
$reporter = $null
$finalized = $false
try {
  if ($result.executable -ne $resolved) { throw 'Executable does not match the reviewed installation.' }
  if ($result.attempt_id -notmatch '^[a-f0-9]{32}$' -or -not [IO.Path]::IsPathRooted($result.receipt_home)) {
    throw 'A compatible CLI with an uninstall receipt is required.'
  }
  $allowedCleanup = @("${resolved}.previous", "${resolved}.update.json", "${resolved}.install.json", "${resolved}.uninstall.sh", "${resolved}.uninstall.ps1")
  foreach ($file in @($resolved) + @($result.cleanup_files)) {
    if ($file -ne $resolved -and $allowedCleanup -notcontains $file) { throw 'Unexpected installation cleanup path.' }
    if (Test-Path -LiteralPath $file) {
      $item = Get-Item -LiteralPath $file -Force
      if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Installation sidecar must be a regular file.' }
    }
  }
  # Keep a trusted copy only until final observation. No credentials enter it.
  $temporary = Join-Path ([IO.Path]::GetTempPath()) ('eigenflux-uninstall-' + [Guid]::NewGuid().ToString('N'))
  [IO.Directory]::CreateDirectory($temporary) | Out-Null
  $reporter = Join-Path $temporary 'eigenflux.exe'
  Copy-Item -LiteralPath $resolved -Destination $reporter -ErrorAction Stop
  # The trusted copy reacquires installation/watch locks and checks file hashes.
  $call = Invoke-EigenFluxJson $reporter @('--homedir', $result.receipt_home, 'uninstall-finalize', '--attempt', $result.attempt_id, '--remove-original', '--format', 'json')
  if ($call.ExitCode -ne 0) { throw 'Final uninstall observation failed; inspect the retained receipt.' }
  $final = $call.Output | ConvertFrom-Json
  $finalized = $true
  $result.executable_removal_pending = Test-Path -LiteralPath $resolved
  $result | Add-Member -NotePropertyName result -NotePropertyValue $final.result -Force
  $result | ConvertTo-Json -Depth 10
  if ($final.result -ne 'removed') { exit 1 }
} catch {
  # Observe retained original files as partial, never as a successful removal.
  if ($reporter -and (Test-Path -LiteralPath $reporter) -and -not $finalized) {
    $call = Invoke-EigenFluxJson $reporter @('--homedir', $result.receipt_home, 'uninstall-finalize', '--attempt', $result.attempt_id, '--format', 'json')
    if ($call.ExitCode -eq 0) { $call.Output }
  }
  Write-Error 'Uninstall incomplete. Inspect the retained audit and remaining installation files.'
  exit 1
} finally {
  if ($reporter -and (Test-Path -LiteralPath $reporter)) { Remove-Item -LiteralPath $reporter -ErrorAction SilentlyContinue }
  if ($temporary -and (Test-Path -LiteralPath $temporary)) { Remove-Item -LiteralPath $temporary -ErrorAction SilentlyContinue }
}
