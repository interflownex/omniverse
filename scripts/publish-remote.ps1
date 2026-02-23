$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$remoteDir = Join-Path $root 'runtime\\remote'
if (-not (Test-Path $remoteDir)) { New-Item -ItemType Directory -Path $remoteDir | Out-Null }

$cloudflared = (Get-Command cloudflared -ErrorAction SilentlyContinue).Source
if (-not $cloudflared) {
  $candidates = @(
    'C:\Program Files (x86)\cloudflared\cloudflared.exe',
    'C:\Program Files\cloudflared\cloudflared.exe'
  )
  foreach ($c in $candidates) {
    if (Test-Path $c) { $cloudflared = $c; break }
  }
}
if (-not $cloudflared) {
  Write-Error '[publish-remote] cloudflared not found. Run scripts/bootstrap.ps1 first.'
}

& "$PSScriptRoot\unpublish-remote.ps1" 2>$null

$logFile = Join-Path $remoteDir 'cloudflared.log'
$pidFile = Join-Path $remoteDir 'cloudflared.pid'
$urlFile = Join-Path $remoteDir 'public-url.txt'

$proc = Start-Process -FilePath $cloudflared -ArgumentList 'tunnel', '--url', 'http://127.0.0.1:8080', '--no-autoupdate' -PassThru -WindowStyle Hidden -RedirectStandardOutput $logFile -RedirectStandardError $logFile
$proc.Id | Set-Content -Path $pidFile -Encoding UTF8

$url = $null
for ($i = 0; $i -lt 40; $i++) {
  Start-Sleep -Seconds 1
  if (Test-Path $logFile) {
    $match = Select-String -Path $logFile -Pattern 'https://[-a-zA-Z0-9]+\.trycloudflare\.com' -AllMatches | Select-Object -Last 1
    if ($match) {
      $url = $match.Matches[0].Value
      break
    }
  }
}

if (-not $url) {
  Write-Error '[publish-remote] tunnel started but public URL was not found in logs.'
}

$url | Set-Content -Path $urlFile -Encoding UTF8
Write-Host "[publish-remote] public URL: $url"
