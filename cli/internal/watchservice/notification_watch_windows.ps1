param([Parameter(Mandatory=$true)][string]$RequestFile)
$ErrorActionPreference = 'Stop'
$request = Get-Content -LiteralPath $RequestFile -Raw -Encoding UTF8 | ConvertFrom-Json
Get-ChildItem Env: | Where-Object { $_.Name -like 'EIGENFLUX_*' } | ForEach-Object { Remove-Item -LiteralPath ('Env:' + $_.Name) }
$env:EIGENFLUX_WATCH_EXPECTED_AGENT = [string]$request.agent_id
$env:EIGENFLUX_WATCH_EXPECTED_PRINCIPAL = [string]$request.principal_id
$log = [string]$request.log
if ((Test-Path -LiteralPath $log) -and (Get-Item -LiteralPath $log).Length -gt 4MB) {
    Move-Item -LiteralPath $log -Destination ($log + '.previous') -Force
}
& ([string]$request.cli) --homedir ([string]$request.home) --server ([string]$request.server) watch --notifications-only --format json >> $log 2>&1
exit $LASTEXITCODE
