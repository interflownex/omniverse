$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Write-Host '[bootstrap] root:' $root

foreach ($dir in @('data','runtime','runtime\\remote','logs')) {
  $path = Join-Path $root $dir
  if (-not (Test-Path $path)) { New-Item -ItemType Directory -Path $path | Out-Null }
}

if (Get-Command winget -ErrorAction SilentlyContinue) {
  if (-not (Get-Command rustc -ErrorAction SilentlyContinue)) {
    Write-Host '[bootstrap] installing Rust toolchain via winget...'
    winget install --id Rustlang.Rustup -e --accept-package-agreements --accept-source-agreements
  }
  if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
    Write-Host '[bootstrap] installing Node.js LTS via winget...'
    winget install --id OpenJS.NodeJS.LTS -e --accept-package-agreements --accept-source-agreements
  }
  if (-not (Get-Command cloudflared -ErrorAction SilentlyContinue)) {
    Write-Host '[bootstrap] installing cloudflared via winget...'
    winget install --id Cloudflare.cloudflared -e --accept-package-agreements --accept-source-agreements
  }
} else {
  Write-Warning '[bootstrap] winget not available; install rust/node/cloudflared manually.'
}

Write-Host '[bootstrap] done. run scripts/up.ps1 next.'
