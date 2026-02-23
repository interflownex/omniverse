$ErrorActionPreference = 'Stop'
$urls = @('https://example.com', 'https://www.cloudflare.com', 'https://www.microsoft.com')
$results = @()
foreach ($url in $urls) {
  try {
    $resp = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 20
    $results += [pscustomobject]@{ URL = $url; Status = 'OK'; Code = $resp.StatusCode }
  } catch {
    $results += [pscustomobject]@{ URL = $url; Status = 'FAIL'; Code = $_.Exception.Message }
  }
}
$results | Format-Table -AutoSize
if ($results.Status -contains 'FAIL') {
  Write-Error '[validate-host-internet] host internet test failed.'
}
Write-Host '[validate-host-internet] host internet remains available.'
