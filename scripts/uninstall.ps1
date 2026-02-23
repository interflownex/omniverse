$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot

& "$PSScriptRoot\revoke-remote-tester.ps1" 2>$null
& "$PSScriptRoot\unpublish-remote.ps1" 2>$null
& "$PSScriptRoot\down.ps1" 2>$null

foreach ($path in @('data','runtime','logs','core\\target')) {
  $full = Join-Path $root $path
  if (Test-Path $full) { Remove-Item $full -Recurse -Force }
}
Write-Host '[uninstall] nexora local artifacts removed.'
Write-Host '[uninstall] no global proxy or firewall changes were applied.'
