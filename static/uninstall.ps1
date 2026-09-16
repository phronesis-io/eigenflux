param(
  [Parameter(Mandatory=$true)][string]$CliPath,
  [Parameter(ValueFromRemainingArguments=$true)][string[]]$UninstallArgs
)
$ErrorActionPreference = 'Stop'
$resolved = (Resolve-Path -LiteralPath $CliPath).Path
$raw = & $resolved uninstall @UninstallArgs --format json
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$result = $raw | ConvertFrom-Json
if ($result.apply -and $result.executable_removal_pending) {
  if ($result.executable -ne $resolved) { throw 'Executable does not match the reviewed installation.' }
  $allowedCleanup = @("${resolved}.previous", "${resolved}.update.json", "${resolved}.install.json")
  foreach ($file in $result.cleanup_files) {
    if ($allowedCleanup -notcontains $file) { throw 'Unexpected installation cleanup path.' }
    if (Test-Path -LiteralPath $file) {
      $item = Get-Item -LiteralPath $file -Force
      if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Installation sidecar must be a regular file.' }
    }
  }
  Remove-Item -LiteralPath $resolved -ErrorAction Stop
  foreach ($file in $result.cleanup_files) {
    if (Test-Path -LiteralPath $file) { Remove-Item -LiteralPath $file -ErrorAction Stop }
  }
  $result.executable_removal_pending = $false
}
$result | ConvertTo-Json -Depth 10
