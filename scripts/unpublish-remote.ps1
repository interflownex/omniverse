$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $root 'runtime\\remote\\cloudflared.pid'
$urlFile = Join-Path $root 'runtime\\remote\\public-url.txt'

if (Test-Path $pidFile) {
  $pid = Get-Content $pidFile | Select-Object -First 1
  if ($pid) {
    $proc = Get-Process -Id $pid -ErrorAction SilentlyContinue
    if ($proc) { Stop-Process -Id $pid -Force }
  }
  Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
}
if (Test-Path $urlFile) { Remove-Item $urlFile -Force }
Write-Host '[unpublish-remote] remote tunnel stopped.'
