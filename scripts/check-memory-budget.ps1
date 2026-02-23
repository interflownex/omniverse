param([int]$LimitMB = 13)
$ErrorActionPreference = 'Stop'

$proc = Get-Process nexora-core -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $proc) {
  Write-Error '[check-memory-budget] nexora-core process not found. Start the service first.'
}

$rssMB = [math]::Round($proc.WorkingSet64 / 1MB, 2)
Write-Host "[check-memory-budget] RSS: $rssMB MB | Limit: $LimitMB MB"
if ($rssMB -gt $LimitMB) {
  Write-Error "[check-memory-budget] memory budget exceeded: $rssMB MB > $LimitMB MB"
}
Write-Host '[check-memory-budget] budget respected.'
