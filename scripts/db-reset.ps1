$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$dataDir = Join-Path $root 'data'
if (Test-Path $dataDir) {
  Remove-Item $dataDir -Recurse -Force
}
New-Item -ItemType Directory -Path $dataDir | Out-Null
Write-Host '[db-reset] data directory reset.'
