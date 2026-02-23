param(
  [string]$AdminEmail = 'admin@nexora.local',
  [string]$AdminPassword = 'admin247',
  [string]$TesterEmail = 'tester.remote@nexora.local',
  [int]$ExpiresHours = 24
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$outFile = Join-Path $root 'runtime\\remote\\tester-credentials.txt'

$login = Invoke-RestMethod -Method POST -Uri 'http://127.0.0.1:8080/api/v24.7/auth/login' -Body (@{email=$AdminEmail;password=$AdminPassword} | ConvertTo-Json) -ContentType 'application/json'
$headers = @{ Authorization = "Bearer $($login.access_token)" }

$provision = Invoke-RestMethod -Method POST -Uri 'http://127.0.0.1:8080/api/v24.7/test-access/provision' -Headers $headers -Body (@{email=$TesterEmail;expires_hours=$ExpiresHours} | ConvertTo-Json) -ContentType 'application/json'

@(
  "NEXORA Remote Tester Credentials",
  "email=$($provision.email)",
  "password=$($provision.password)",
  "expires_at=$($provision.expires_at)",
  "tenant_id=$($provision.tenant_id)"
) | Set-Content -Path $outFile -Encoding UTF8

Write-Host "[create-remote-tester] credentials saved: $outFile"
