$ErrorActionPreference = 'Continue'
$outPath = 'd:\Code\CloudHelper\probe_node\real_nic_diag.txt'
$idx = 18
$targetIP = '198.18.0.1'

$lines = New-Object System.Collections.Generic.List[string]
$lines.Add('TS=' + (Get-Date).ToString('s'))
$lines.Add('IDX=' + $idx)
$lines.Add('TARGET_IP=' + $targetIP)

try {
  $ad = Get-NetAdapter -InterfaceIndex $idx -ErrorAction Stop
  $lines.Add('ADAPTER_NAME=' + $ad.Name)
  $lines.Add('ADAPTER_STATUS=' + $ad.Status)
  $lines.Add('ADAPTER_MAC=' + $ad.MacAddress)
} catch {
  $lines.Add('ADAPTER_ERROR=' + $_.Exception.Message)
}

try {
  $ipObj = Get-NetIPAddress -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $targetIP } | Select-Object -First 1
  if ($ipObj) {
    $lines.Add('ADDR_STATE=' + $ipObj.AddressState)
    $lines.Add('ADDR_PREFIX=' + $ipObj.PrefixLength)
    $lines.Add('ADDR_SKIPASSOURCE=' + $ipObj.SkipAsSource)
    $lines.Add('ADDR_VALID_LIFE=' + $ipObj.ValidLifetime)
    $lines.Add('ADDR_PREFERRED_LIFE=' + $ipObj.PreferredLifetime)
  } else {
    $lines.Add('ADDR_STATE=MISSING')
  }
} catch {
  $lines.Add('ADDR_QUERY_ERROR=' + $_.Exception.Message)
}

try {
  $ipIf = Get-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($ipIf) {
    $lines.Add('IPIF_DAD=' + $ipIf.DadTransmits)
    $lines.Add('IPIF_NLMTU=' + $ipIf.NlMtu)
    $lines.Add('IPIF_CONNSTATE=' + $ipIf.ConnectionState)
    $lines.Add('IPIF_FORWARDING=' + $ipIf.Forwarding)
    $lines.Add('IPIF_WEAKHOSTSEND=' + $ipIf.WeakHostSend)
    $lines.Add('IPIF_WEAKHOSTRECV=' + $ipIf.WeakHostReceive)
  } else {
    $lines.Add('IPIF=MISSING')
  }
} catch {
  $lines.Add('IPIF_ERROR=' + $_.Exception.Message)
}

try {
  $udp = [System.Net.Sockets.UdpClient]::new([System.Net.IPEndPoint]::new([System.Net.IPAddress]::Parse($targetIP), 0))
  $udp.Close()
  $lines.Add('BIND=OK')
} catch {
  $lines.Add('BIND=FAIL')
  $lines.Add('BIND_ERR=' + $_.Exception.Message)
}

$lines | Out-File -FilePath $outPath -Encoding UTF8
