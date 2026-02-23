$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path (Join-Path $root 'data'))) { New-Item -ItemType Directory -Path (Join-Path $root 'data') | Out-Null }

Write-Host '[db-bootstrap] ensuring schema via nexora-core startup...'
if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) {
  Write-Host '[db-bootstrap] cargo not found. Install Rust with scripts/bootstrap.ps1 first.'
  exit 1
}

$env:NEXORA_ROOT = $root
cargo run --manifest-path "$root\core\Cargo.toml" --quiet
