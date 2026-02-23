param(
  [string]$BaseUrl
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$runtimeDir = Join-Path $root 'runtime'
$baseUrlFile = Join-Path $runtimeDir 'nexora-base-url.txt'
$remoteDir = Join-Path $root 'runtime\\remote'
if (-not (Test-Path $remoteDir)) { New-Item -ItemType Directory -Path $remoteDir | Out-Null }

if (-not $BaseUrl) {
  if ($env:NEXORA_BASE_URL) {
    $BaseUrl = $env:NEXORA_BASE_URL
  } elseif (Test-Path $baseUrlFile) {
    $BaseUrl = (Get-Content $baseUrlFile | Select-Object -First 1)
  } else {
    $BaseUrl = 'http://127.0.0.1:8080'
  }
}
$BaseUrl = $BaseUrl.ToString().Trim().TrimEnd('/')
if (-not $BaseUrl) {
  Write-Error '[publish-remote] invalid BaseUrl.'
}

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

$outFile = Join-Path $remoteDir 'cloudflared.out.log'
$errFile = Join-Path $remoteDir 'cloudflared.err.log'
$pidFile = Join-Path $remoteDir 'cloudflared.pid'
$urlFile = Join-Path $remoteDir 'public-url.txt'
$originFile = Join-Path $remoteDir 'origin-url.txt'

$proc = Start-Process -FilePath $cloudflared -ArgumentList 'tunnel', '--url', $BaseUrl, '--no-autoupdate' -PassThru -WindowStyle Hidden -RedirectStandardOutput $outFile -RedirectStandardError $errFile
$proc.Id | Set-Content -Path $pidFile -Encoding UTF8

$url = $null
for ($i = 0; $i -lt 45; $i++) {
  Start-Sleep -Seconds 1
  $logFiles = @($outFile, $errFile) | Where-Object { Test-Path $_ }
  if ($logFiles.Count -gt 0) {
    $match = Select-String -Path $logFiles -Pattern 'https://[-a-zA-Z0-9]+\.trycloudflare\.com' -AllMatches | Select-Object -Last 1
    if ($match -and $match.Matches.Count -gt 0) {
      $url = $match.Matches[$match.Matches.Count - 1].Value
      if ($url) { break }
    }
  }
}

if (-not $url) {
  $stderr = if (Test-Path $errFile) { Get-Content $errFile -Raw } else { '' }
  Write-Error "[publish-remote] tunnel started but public URL was not found in logs. $stderr"
}

$url | Set-Content -Path $urlFile -Encoding UTF8
$BaseUrl | Set-Content -Path $originFile -Encoding UTF8
Write-Host "[publish-remote] origin URL: $BaseUrl"
Write-Host "[publish-remote] public URL: $url"

