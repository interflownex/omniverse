$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) {
  Write-Error '[up] cargo not found. Run scripts/bootstrap.ps1 first.'
}
$env:NEXORA_ROOT = $root
Write-Host '[up] starting nexora-core in foreground...'
cargo run --release --manifest-path "$root\core\Cargo.toml"
