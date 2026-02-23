$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $root 'runtime\nexora-core.pid'
$baseUrlFile = Join-Path $root 'runtime\nexora-base-url.txt'
$stopped = $false

if (Test-Path $pidFile) {
  $targetPid = (Get-Content $pidFile | Select-Object -First 1).ToString().Trim()
  if ($targetPid -match '^\d+$') {
    $proc = Get-Process -Id $targetPid -ErrorAction SilentlyContinue
    if ($proc) {
      Stop-Process -Id $targetPid -Force
      Write-Host "[down] stopped process $targetPid"
      $stopped = $true
    } else {
      Write-Host "[down] process $targetPid not running"
    }
  } else {
    Write-Host "[down] invalid pid value in file: $targetPid"
  }
  Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
}

if (-not $stopped -and (Test-Path $baseUrlFile)) {
  try {
    $baseUrl = (Get-Content $baseUrlFile | Select-Object -First 1).ToString().Trim()
    $port = ([Uri]$baseUrl).Port
    $listenLine = cmd /c "netstat -ano | findstr LISTENING | findstr :$port" | Select-Object -First 1
    if ($listenLine) {
      $parts = ($listenLine -split '\s+') | Where-Object { $_ -ne '' }
      $ownerPid = $parts[-1]
      if ($ownerPid -match '^\d+$' -and $ownerPid -ne '0') {
        $ownerProc = Get-Process -Id $ownerPid -ErrorAction SilentlyContinue
        if ($ownerProc) {
          Stop-Process -Id $ownerPid -Force
          Write-Host "[down] stopped process $ownerPid from port $port"
          $stopped = $true
        }
      }
    }
  } catch {
    Write-Host "[down] fallback stop by base URL failed: $($_.Exception.Message)"
  }
}

if (-not $stopped) {
  Write-Host '[down] no running nexora-core process detected.'
}
