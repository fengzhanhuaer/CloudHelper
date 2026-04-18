param(
  [string]$Base = 'http://127.0.0.1:16033'
)

$paths = @('/', '/index.html', '/assets/index.77a91ec0.js', '/healthz')
foreach ($p in $paths) {
  $u = $Base.TrimEnd('/') + $p
  Write-Host "=== $u ==="
  try {
    $resp = Invoke-WebRequest -Uri $u -UseBasicParsing -TimeoutSec 8
    Write-Host ("STATUS=" + [int]$resp.StatusCode)
    $ct = $resp.Headers['Content-Type']
    if ($ct) { Write-Host ("CONTENT-TYPE=" + $ct) }
    $body = $resp.Content
    if ($body.Length -gt 220) { $body = $body.Substring(0,220) + ' ...' }
    Write-Host "BODY-PREVIEW:"
    Write-Host $body
  } catch {
    $ex = $_.Exception
    if ($ex.Response) {
      $code = [int]$ex.Response.StatusCode
      Write-Host ("STATUS=" + $code)
      $stream = $ex.Response.GetResponseStream()
      if ($stream) {
        $reader = New-Object System.IO.StreamReader($stream)
        $txt = $reader.ReadToEnd()
        if ($txt.Length -gt 400) { $txt = $txt.Substring(0,400) + ' ...' }
        Write-Host "BODY-PREVIEW:"
        Write-Host $txt
      }
    } else {
      Write-Host ("ERROR=" + $ex.Message)
    }
  }
  Write-Host ''
}
