param(
  [string]$AdminEmail = 'admin@nexora.local',
  [string]$AdminPassword = 'admin247',
  [string]$TesterEmail = 'tester.remote@nexora.local',
  [int]$ExpiresHours = 24,
  [string]$BaseUrl
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$runtimeDir = Join-Path $root 'runtime'
$baseUrlFile = Join-Path $runtimeDir 'nexora-base-url.txt'
$outFile = Join-Path $root 'runtime\\remote\\tester-credentials.txt'

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
  throw '[create-remote-tester] invalid BaseUrl.'
}

try {
  $login = Invoke-RestMethod -Method POST -Uri "$BaseUrl/api/v24.7/auth/login" -Body (@{email=$AdminEmail;password=$AdminPassword} | ConvertTo-Json) -ContentType 'application/json' -TimeoutSec 20
  $headers = @{ Authorization = "Bearer $($login.access_token)" }

  $provision = Invoke-RestMethod -Method POST -Uri "$BaseUrl/api/v24.7/test-access/provision" -Headers $headers -Body (@{email=$TesterEmail;expires_hours=$ExpiresHours} | ConvertTo-Json) -ContentType 'application/json' -TimeoutSec 20
} catch {
  throw "[create-remote-tester] failed to provision tester. Ensure nexora-core is running on $BaseUrl and admin credentials are valid. Details: $($_.Exception.Message)"
}

@(
  "NEXORA Remote Tester Credentials",
  "email=$($provision.email)",
  "password=$($provision.password)",
  "expires_at=$($provision.expires_at)",
  "tenant_id=$($provision.tenant_id)"
) | Set-Content -Path $outFile -Encoding UTF8

Write-Host "[create-remote-tester] credentials saved: $outFile"
