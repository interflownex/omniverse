param([switch]$Start)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
& "$PSScriptRoot\down.ps1"
& "$PSScriptRoot\unpublish-remote.ps1" 2>$null

foreach ($path in @('data','logs','runtime\\remote')) {
  $full = Join-Path $root $path
  if (Test-Path $full) { Remove-Item $full -Recurse -Force }
}
New-Item -ItemType Directory -Path (Join-Path $root 'data') -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $root 'runtime\\remote') -Force | Out-Null
Write-Host '[reset] local runtime reset complete.'
if ($Start) { & "$PSScriptRoot\up.ps1" }
