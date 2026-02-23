$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $root 'runtime\\remote\\cloudflared.pid'
$urlFile = Join-Path $root 'runtime\\remote\\public-url.txt'

if (Test-Path $pidFile) {
  $targetPid = (Get-Content $pidFile | Select-Object -First 1).ToString().Trim()
  if ($targetPid -match '^\d+$') {
    $proc = Get-Process -Id $targetPid -ErrorAction SilentlyContinue
    if ($proc) { Stop-Process -Id $targetPid -Force }
  }
  Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
}
if (Test-Path $urlFile) { Remove-Item $urlFile -Force }
Write-Host '[unpublish-remote] remote tunnel stopped.'
