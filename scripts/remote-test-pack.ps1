$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot

& "$PSScriptRoot\publish-remote.ps1"
& "$PSScriptRoot\create-remote-tester.ps1"

$urlFile = Join-Path $root 'runtime\\remote\\public-url.txt'
$credFile = Join-Path $root 'runtime\\remote\\tester-credentials.txt'

Write-Host ''
Write-Host '=== NEXORA REMOTE TEST PACK ==='
if (Test-Path $urlFile) { Write-Host 'Public URL:' (Get-Content $urlFile | Select-Object -First 1) }
if (Test-Path $credFile) { Get-Content $credFile | ForEach-Object { Write-Host $_ } }
Write-Host '==============================='
