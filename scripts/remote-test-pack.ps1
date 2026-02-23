param(
  [string]$BaseUrl,
  [switch]$SkipAutoStart
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$runtimeDir = Join-Path $root 'runtime'
$remoteDir = Join-Path $runtimeDir 'remote'
$baseUrlFile = Join-Path $runtimeDir 'nexora-base-url.txt'
$corePidFile = Join-Path $runtimeDir 'nexora-core.pid'
$coreOutLog = Join-Path $runtimeDir 'nexora-core.out.log'
$coreErrLog = Join-Path $runtimeDir 'nexora-core.err.log'

if (-not (Test-Path $runtimeDir)) { New-Item -ItemType Directory -Path $runtimeDir | Out-Null }
if (-not (Test-Path $remoteDir)) { New-Item -ItemType Directory -Path $remoteDir | Out-Null }

function Resolve-ConfiguredBaseUrl([string]$explicitBaseUrl, [string]$baseFilePath) {
  if ($explicitBaseUrl) { return $explicitBaseUrl.ToString().Trim().TrimEnd('/') }
  if ($env:NEXORA_BASE_URL) { return $env:NEXORA_BASE_URL.ToString().Trim().TrimEnd('/') }
  if (Test-Path $baseFilePath) {
    $fromFile = (Get-Content $baseFilePath | Select-Object -First 1).ToString().Trim()
    if ($fromFile) { return $fromFile.TrimEnd('/') }
  }
  return $null
}

function Test-NexoraHealthy([string]$url, [int]$timeoutSec = 3) {
  if (-not $url) { return $false }
  try {
    $resp = Invoke-RestMethod -Method GET -Uri "$url/health" -TimeoutSec $timeoutSec
    return ($resp.status -eq 'healthy')
  } catch {
    return $false
  }
}

function Test-LocalPortFree([int]$port) {
  $listener = $null
  try {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $port)
    $listener.Start()
    return $true
  } catch {
    return $false
  } finally {
    if ($listener) { $listener.Stop() }
  }
}

function Start-NexoraCore([string]$workspaceRoot, [int]$port, [string]$pidFile, [string]$outLog, [string]$errLog, [string]$baseFilePath) {
  $exe = Join-Path $workspaceRoot 'core\target\release\nexora-core.exe'
  if (-not (Test-Path $exe)) {
    if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) {
      throw '[remote-test-pack] cargo not found and nexora-core.exe missing. Run scripts/bootstrap.ps1 first.'
    }
    & cargo build --release --manifest-path (Join-Path $workspaceRoot 'core\Cargo.toml')
    if ($LASTEXITCODE -ne 0) {
      throw "[remote-test-pack] failed building nexora-core (exit $LASTEXITCODE)."
    }
  }

  $oldRoot = $env:NEXORA_ROOT
  $oldBind = $env:NEXORA_BIND
  try {
    $env:NEXORA_ROOT = $workspaceRoot
    $env:NEXORA_BIND = "127.0.0.1:$port"
    $proc = Start-Process -FilePath $exe -WorkingDirectory $workspaceRoot -PassThru -WindowStyle Hidden -RedirectStandardOutput $outLog -RedirectStandardError $errLog
  } finally {
    if ($null -eq $oldRoot) { Remove-Item Env:NEXORA_ROOT -ErrorAction SilentlyContinue } else { $env:NEXORA_ROOT = $oldRoot }
    if ($null -eq $oldBind) { Remove-Item Env:NEXORA_BIND -ErrorAction SilentlyContinue } else { $env:NEXORA_BIND = $oldBind }
  }
  $proc.Id | Set-Content -Path $pidFile -Encoding UTF8

  $url = "http://127.0.0.1:$port"
  for ($i = 0; $i -lt 45; $i++) {
    if (Test-NexoraHealthy -url $url -timeoutSec 2) {
      $url | Set-Content -Path $baseFilePath -Encoding UTF8
      return $url
    }
    Start-Sleep -Seconds 1
  }

  $stderr = if (Test-Path $errLog) { (Get-Content $errLog -Tail 30) -join [Environment]::NewLine } else { '' }
  throw "[remote-test-pack] nexora-core did not become healthy on $url. $stderr"
}

$candidateUrls = @()
$configured = Resolve-ConfiguredBaseUrl -explicitBaseUrl $BaseUrl -baseFilePath $baseUrlFile
if ($configured) { $candidateUrls += $configured }
$candidateUrls += @('http://127.0.0.1:8080', 'http://127.0.0.1:18080')
$candidateUrls = $candidateUrls | Where-Object { $_ } | Select-Object -Unique

$activeBaseUrl = $null
foreach ($candidate in $candidateUrls) {
  if (Test-NexoraHealthy -url $candidate -timeoutSec 3) {
    $activeBaseUrl = $candidate
    break
  }
}

if (-not $activeBaseUrl) {
  if ($SkipAutoStart) {
    throw '[remote-test-pack] no healthy local Nexora instance found and auto-start is disabled.'
  }

  $portCandidates = @(8080, 18080, 18081, 18082, 18083, 18084, 18085)
  $targetPort = $null
  foreach ($p in $portCandidates) {
    if (Test-LocalPortFree -port $p) { $targetPort = $p; break }
  }
  if (-not $targetPort) {
    throw '[remote-test-pack] no free local port available to auto-start nexora-core.'
  }
  $activeBaseUrl = Start-NexoraCore -workspaceRoot $root -port $targetPort -pidFile $corePidFile -outLog $coreOutLog -errLog $coreErrLog -baseFilePath $baseUrlFile
} else {
  $activeBaseUrl | Set-Content -Path $baseUrlFile -Encoding UTF8
}

try {
  & "$PSScriptRoot\publish-remote.ps1" -BaseUrl $activeBaseUrl
} catch {
  throw "[remote-test-pack] publish step failed. Details: $($_.Exception.Message)"
}

try {
  & "$PSScriptRoot\create-remote-tester.ps1" -BaseUrl $activeBaseUrl
} catch {
  throw "[remote-test-pack] create tester step failed. Details: $($_.Exception.Message)"
}

$urlFile = Join-Path $remoteDir 'public-url.txt'
$credFile = Join-Path $remoteDir 'tester-credentials.txt'
$originFile = Join-Path $remoteDir 'origin-url.txt'

Write-Host ''
Write-Host '=== NEXORA REMOTE TEST PACK ==='
Write-Host 'Local API Base:' $activeBaseUrl
if (Test-Path $originFile) { Write-Host 'Tunnel Origin:' (Get-Content $originFile | Select-Object -First 1) }
if (Test-Path $urlFile) { Write-Host 'Public URL:' (Get-Content $urlFile | Select-Object -First 1) }
if (Test-Path $credFile) { Get-Content $credFile | ForEach-Object { Write-Host $_ } }
Write-Host '==============================='
