Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Log {
  param([string]$Message)
  Write-Host "[cloudhelper-probe-node-install] $Message"
}

function Fail {
  param([string]$Message)
  throw "[cloudhelper-probe-node-install][ERROR] $Message"
}

function Require-Admin {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($identity)
  if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail "please run powershell as administrator"
  }
}

function Escape-XmlValue {
  param([string]$Value)
  if ($null -eq $Value) {
    return ""
  }
  $escaped = [Security.SecurityElement]::Escape($Value)
  if ($null -eq $escaped) {
    return ""
  }
  return $escaped
}

function Invoke-GitHubApiJson {
  param([string]$Url)
  $headers = @{
    "Accept" = "application/vnd.github+json"
    "User-Agent" = "cloudhelper-probe-node-install"
  }
  if ($env:GITHUB_TOKEN) {
    $headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
  }
  return Invoke-RestMethod -Method Get -Uri $Url -Headers $headers -TimeoutSec 60
}

function Invoke-DownloadFile {
  param(
    [string]$Url,
    [string]$OutFile
  )
  $headers = @{
    "User-Agent" = "cloudhelper-probe-node-install"
  }
  if ($env:GITHUB_TOKEN) {
    $headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
  }
  Invoke-WebRequest -UseBasicParsing -Uri $Url -Headers $headers -OutFile $OutFile -TimeoutSec 300
}

function Resolve-ArchInfo {
  $arch = ""

  # PowerShell 5.1 on older .NET may not expose RuntimeInformation.ProcessArchitecture.
  try {
    $runtimeInfoType = [Type]::GetType("System.Runtime.InteropServices.RuntimeInformation")
    if ($runtimeInfoType) {
      $processArchProp = $runtimeInfoType.GetProperty("ProcessArchitecture", [Reflection.BindingFlags]"Public,Static")
      if ($processArchProp) {
        $archValue = $processArchProp.GetValue($null, $null)
        if ($archValue) {
          $arch = [string]$archValue
        }
      }
    }
  } catch {}

  if (-not $arch) {
    if ($env:PROCESSOR_ARCHITEW6432) {
      $arch = [string]$env:PROCESSOR_ARCHITEW6432
    } elseif ($env:PROCESSOR_ARCHITECTURE) {
      $arch = [string]$env:PROCESSOR_ARCHITECTURE
    }
  }

  if (-not $arch) {
    if ([IntPtr]::Size -eq 8) {
      $arch = "x64"
    } else {
      $arch = "x86"
    }
  }

  $arch = $arch.ToLowerInvariant()
  switch ($arch) {
    { $_ -in @("x64", "amd64", "x86_64") } {
      return @{
        Name = "amd64"
        MatchTokens = @("amd64", "x86_64", "x64")
        WinSWAsset = "WinSW-x64.exe"
      }
    }
    { $_ -in @("arm64", "aarch64") } {
      return @{
        Name = "arm64"
        MatchTokens = @("arm64", "aarch64")
        WinSWAsset = "WinSW-arm64.exe"
      }
    }
    { $_ -in @("x86", "386", "i386") } {
      return @{
        Name = "386"
        MatchTokens = @("386", "i386", "x86")
        WinSWAsset = "WinSW-x86.exe"
      }
    }
    default {
      return @{
        Name = $arch
        MatchTokens = @($arch)
        WinSWAsset = "WinSW-x64.exe"
      }
    }
  }
}

function Select-ProbeAsset {
  param(
    [object]$Release,
    [hashtable]$ArchInfo,
    [string]$AssetNameOverride
  )
  if (-not $Release -or -not $Release.assets) {
    Fail "release assets are empty"
  }

  if ($AssetNameOverride) {
    foreach ($asset in $Release.assets) {
      if ($asset.name -eq $AssetNameOverride) {
        return $asset
      }
    }
    Fail "asset not found: $AssetNameOverride"
  }

  # Prefer exact release naming first:
  # cloudhelper-probe-node-windows-<arch>.exe
  $expectedNames = @(
    "cloudhelper-probe-node-windows-$($ArchInfo.Name).exe"
  )
  foreach ($expectedName in $expectedNames) {
    foreach ($asset in $Release.assets) {
      if ([string]$asset.name -eq $expectedName) {
        return $asset
      }
    }
  }

  $probeAssets = @()
  foreach ($asset in $Release.assets) {
    $name = [string]$asset.name
    $lower = $name.ToLowerInvariant()
    if (($lower.Contains("probe-node") -or $lower.Contains("probe_node")) -and $lower.Contains("windows")) {
      $probeAssets += $asset
    }
  }

  foreach ($token in $ArchInfo.MatchTokens) {
    foreach ($asset in $probeAssets) {
      $lower = ([string]$asset.name).ToLowerInvariant()
      if ($lower.Contains($token)) {
        return $asset
      }
    }
  }

  if ($probeAssets.Count -gt 0) {
    return $probeAssets[0]
  }

  $assetNames = @($Release.assets | ForEach-Object { $_.name }) -join ", "
  Fail "failed to find windows probe_node asset for arch=$($ArchInfo.Name), assets=[$assetNames]"
}

function Write-ServiceXml {
  param(
    [string]$XmlPath,
    [string]$ServiceName,
    [string]$ServiceDisplayName,
    [string]$NodeID,
    [string]$NodeSecret,
    [string]$ControllerURL,
    [string]$ListenAddr
  )

  $serviceID = Escape-XmlValue $ServiceName
  $serviceNameEscaped = Escape-XmlValue $ServiceDisplayName
  $envLines = @()
  if ($NodeID) {
    $envLines += "  <env name=""PROBE_NODE_ID"" value=""$(Escape-XmlValue $NodeID)"" />"
  }
  if ($NodeSecret) {
    $envLines += "  <env name=""PROBE_NODE_SECRET"" value=""$(Escape-XmlValue $NodeSecret)"" />"
  }
  if ($ControllerURL) {
    $envLines += "  <env name=""PROBE_CONTROLLER_URL"" value=""$(Escape-XmlValue $ControllerURL)"" />"
  }
  if ($ListenAddr) {
    $envLines += "  <env name=""PROBE_NODE_LISTEN"" value=""$(Escape-XmlValue $ListenAddr)"" />"
  }
  $envBlock = ($envLines -join [Environment]::NewLine)

  $xml = @"
<service>
  <id>$serviceID</id>
  <name>$serviceNameEscaped</name>
  <description>CloudHelper Probe Node</description>
  <executable>%BASE%\probe_node.exe</executable>
  <workingdirectory>%BASE%</workingdirectory>
$envBlock
  <logpath>%BASE%\logs</logpath>
  <log mode="roll" />
  <onfailure action="restart" delay="5 sec" />
</service>
"@
  [System.IO.File]::WriteAllText($XmlPath, $xml, [Text.Encoding]::UTF8)
}

Require-Admin

$releaseRepo = if ($env:RELEASE_REPO) { $env:RELEASE_REPO.Trim() } else { "fengzhanhuaer/CloudHelper" }
$releaseTag = if ($env:RELEASE_TAG) { $env:RELEASE_TAG.Trim() } else { "latest" }
$assetNameOverride = if ($env:ASSET_NAME) { $env:ASSET_NAME.Trim() } else { "" }

$installDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR.Trim() } else { "C:\Tools" }
$serviceName = if ($env:SERVICE_NAME) { $env:SERVICE_NAME.Trim() } else { "probe_node" }
$serviceDisplayName = if ($env:SERVICE_DISPLAY_NAME) { $env:SERVICE_DISPLAY_NAME.Trim() } else { "CloudHelper Probe Node" }
$winswVersion = if ($env:WINSW_VERSION) { $env:WINSW_VERSION.Trim() } else { "v2.12.0" }

$nodeID = if ($env:PROBE_NODE_ID) { $env:PROBE_NODE_ID.Trim() } else { "" }
$nodeSecret = if ($env:PROBE_NODE_SECRET) { $env:PROBE_NODE_SECRET.Trim() } else { "" }
$controllerURL = if ($env:PROBE_CONTROLLER_URL) { $env:PROBE_CONTROLLER_URL.Trim() } else { "" }
$listenAddr = if ($env:PROBE_NODE_LISTEN) { $env:PROBE_NODE_LISTEN.Trim() } else { "" }

if (-not (Test-Path -LiteralPath $installDir)) {
  Write-Log "creating install directory: $installDir"
  New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}

$logsDir = Join-Path $installDir "logs"
if (-not (Test-Path -LiteralPath $logsDir)) {
  New-Item -ItemType Directory -Path $logsDir -Force | Out-Null
}

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cloudhelper-probe-node-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
try {
  $releaseAPI = if ($releaseTag -eq "latest") {
    "https://api.github.com/repos/$releaseRepo/releases/latest"
  } else {
    "https://api.github.com/repos/$releaseRepo/releases/tags/$releaseTag"
  }

  Write-Log "fetching release metadata: $releaseAPI"
  $release = Invoke-GitHubApiJson -Url $releaseAPI
  $tagName = ([string]$release.tag_name).Trim()
  if (-not $tagName) {
    Fail "failed to resolve release tag from github api"
  }

  $archInfo = Resolve-ArchInfo
  $asset = Select-ProbeAsset -Release $release -ArchInfo $archInfo -AssetNameOverride $assetNameOverride
  $assetName = [string]$asset.name
  $assetURL = [string]$asset.browser_download_url
  if (-not $assetURL) {
    Fail "selected asset has empty browser_download_url"
  }

  $assetFile = Join-Path $tmpDir $assetName
  Write-Log "downloading probe asset: $assetName"
  Invoke-DownloadFile -Url $assetURL -OutFile $assetFile

  $winswExePath = Join-Path $installDir "$serviceName-service.exe"
  $winswXmlPath = Join-Path $installDir "$serviceName-service.xml"
  $existingService = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
  if ($existingService) {
    Write-Log "service exists, stopping: $serviceName"
    try { & sc.exe stop $serviceName | Out-Null } catch {}
    Start-Sleep -Seconds 2
  }

  $probeExePath = Join-Path $installDir "probe_node.exe"
  if (Test-Path -LiteralPath $probeExePath) {
    $backupPath = "$probeExePath.bak.$([DateTime]::UtcNow.ToString("yyyyMMddHHmmss"))"
    Write-Log "backup existing binary: $backupPath"
    Move-Item -LiteralPath $probeExePath -Destination $backupPath -Force
  }
  Copy-Item -LiteralPath $assetFile -Destination $probeExePath -Force
  Unblock-File -Path $probeExePath -ErrorAction SilentlyContinue

  $winswURL = "https://github.com/winsw/winsw/releases/download/$winswVersion/$($archInfo.WinSWAsset)"
  $winswTmpFile = Join-Path $tmpDir $archInfo.WinSWAsset
  Write-Log "downloading winsw wrapper: $winswURL"
  Invoke-DownloadFile -Url $winswURL -OutFile $winswTmpFile

  Copy-Item -LiteralPath $winswTmpFile -Destination $winswExePath -Force
  Unblock-File -Path $winswExePath -ErrorAction SilentlyContinue

  Write-ServiceXml -XmlPath $winswXmlPath -ServiceName $serviceName -ServiceDisplayName $serviceDisplayName -NodeID $nodeID -NodeSecret $nodeSecret -ControllerURL $controllerURL -ListenAddr $listenAddr

  if ($existingService) {
    Write-Log "service exists, reinstalling: $serviceName"
    try { & $winswExePath uninstall | Out-Null } catch {}
    if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) {
      try { & sc.exe delete $serviceName | Out-Null } catch {}
    }
    Start-Sleep -Seconds 1
  }

  Write-Log "installing windows service: $serviceName"
  & $winswExePath install | Out-Null
  & sc.exe config $serviceName start= auto | Out-Null
  & $winswExePath start | Out-Null

  Write-Log "installed successfully"
  Write-Log "service: $serviceName"
  Write-Log "binary: $probeExePath"
  Write-Log "release: $tagName"
  Write-Log "asset: $assetName"
  Write-Log "check status: sc query $serviceName"
} finally {
  Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
