$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$dbPath = Join-Path $root 'data\core.db'
if (-not (Test-Path $dbPath)) {
  Write-Error '[db-validate] core.db not found. Run scripts/up.ps1 first.'
}

if (-not (Get-Command sqlite3 -ErrorAction SilentlyContinue)) {
  Write-Host '[db-validate] sqlite3 not found; running file existence validation only.'
  Write-Host '[db-validate] core.db present:' (Test-Path $dbPath)
  exit 0
}

Write-Host '[db-validate] Running integrity checks...'
sqlite3 $dbPath ".read $root/db/sql/05_integrity_checks.sql"
sqlite3 $dbPath ".read $root/db/sql/99_selftest.sql"
