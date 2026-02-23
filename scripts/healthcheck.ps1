$ErrorActionPreference = 'Stop'

function Test-Endpoint($name, $method, $url, $headers = @{}, $body = $null) {
  try {
    if ($null -ne $body) {
      $resp = Invoke-RestMethod -Method $method -Uri $url -Headers $headers -Body ($body | ConvertTo-Json) -ContentType 'application/json' -TimeoutSec 15
    } else {
      $resp = Invoke-RestMethod -Method $method -Uri $url -Headers $headers -TimeoutSec 15
    }
    [pscustomobject]@{ Endpoint = $name; Status = 'OK'; Detail = ($resp | ConvertTo-Json -Compress) }
  } catch {
    [pscustomobject]@{ Endpoint = $name; Status = 'FAIL'; Detail = $_.Exception.Message }
  }
}

$results = @()
$results += Test-Endpoint 'health' 'GET' 'http://127.0.0.1:8080/health'

$login = $null
try {
  $login = Invoke-RestMethod -Method POST -Uri 'http://127.0.0.1:8080/api/v24.7/auth/login' -Body (@{email='admin@nexora.local';password='admin247'} | ConvertTo-Json) -ContentType 'application/json'
  $results += [pscustomobject]@{ Endpoint = 'auth/login'; Status = 'OK'; Detail = 'token issued' }
} catch {
  $results += [pscustomobject]@{ Endpoint = 'auth/login'; Status = 'FAIL'; Detail = $_.Exception.Message }
}

$headers = @{}
if ($login) { $headers.Authorization = "Bearer $($login.access_token)" }
$results += Test-Endpoint 'modules' 'GET' 'http://127.0.0.1:8080/api/v24.7/modules' $headers
$results += Test-Endpoint 'analytics' 'GET' 'http://127.0.0.1:8080/api/v24.7/analytics/overview' $headers
$results += Test-Endpoint 'platform/regions' 'GET' 'http://127.0.0.1:8080/api/v24.7/platform/regions'
$results += Test-Endpoint 'platform/routing' 'GET' 'http://127.0.0.1:8080/api/v24.7/platform/routing'
$results += Test-Endpoint 'platform/replication-status' 'GET' 'http://127.0.0.1:8080/api/v24.7/platform/replication-status'

$results | Format-Table -AutoSize
if ($results.Status -contains 'FAIL') { exit 1 }
