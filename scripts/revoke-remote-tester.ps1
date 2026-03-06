param(
  [string]$AdminEmail = 'admin@nexora.local',
  [string]$AdminPassword = 'admin247',
  [string]$TesterEmail = 'tester.remote@nexora.local'
)
$ErrorActionPreference = 'Stop'

$login = Invoke-RestMethod -Method POST -Uri 'http://127.0.0.1:8080/api/v24.8/auth/login' -Body (@{email=$AdminEmail;password=$AdminPassword} | ConvertTo-Json) -ContentType 'application/json'
$headers = @{ Authorization = "Bearer $($login.access_token)" }

Invoke-RestMethod -Method POST -Uri 'http://127.0.0.1:8080/api/v24.8/test-access/revoke' -Headers $headers -Body (@{email=$TesterEmail} | ConvertTo-Json) -ContentType 'application/json' | Out-Null
Write-Host "[revoke-remote-tester] revoked: $TesterEmail"
