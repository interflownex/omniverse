$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $root 'runtime\nexora-core.pid'
if (-not (Test-Path $pidFile)) {
  Write-Host '[down] no pid file found; nothing to stop.'
  exit 0
}
$pid = Get-Content $pidFile | Select-Object -First 1
if ($pid) {
  $proc = Get-Process -Id $pid -ErrorAction SilentlyContinue
  if ($proc) {
    Stop-Process -Id $pid -Force
    Write-Host "[down] stopped process $pid"
  } else {
    Write-Host "[down] process $pid not running"
  }
}
Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
