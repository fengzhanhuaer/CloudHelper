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

function New-RandomHexToken {
  param([int]$ByteLength = 16)
  if ($ByteLength -le 0) {
    $ByteLength = 8
  }
  $bytes = New-Object byte[] $ByteLength
  $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
  try {
    $rng.GetBytes($bytes)
  } finally {
    $rng.Dispose()
  }
  return [BitConverter]::ToString($bytes).Replace("-", "").ToLowerInvariant()
}

function New-ProbeAuthHeaders {
  param(
    [string]$NodeID,
    [string]$NodeSecret
  )

  $safeNodeID = if ($NodeID) { $NodeID.Trim() } else { "" }
  $safeSecret = if ($NodeSecret) { $NodeSecret.Trim() } else { "" }
  if (-not $safeNodeID -or -not $safeSecret) {
    return $null
  }

  $timestamp = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds().ToString()
  $randomToken = New-RandomHexToken -ByteLength 16
  $payload = "$safeNodeID`n$timestamp`n$randomToken"
  $keyBytes = [Text.Encoding]::UTF8.GetBytes($safeSecret)
  $dataBytes = [Text.Encoding]::UTF8.GetBytes($payload)

  $hmac = [System.Security.Cryptography.HMACSHA256]::new($keyBytes)
  try {
    $sigBytes = $hmac.ComputeHash($dataBytes)
  } finally {
    $hmac.Dispose()
  }
  $signature = [BitConverter]::ToString($sigBytes).Replace("-", "").ToLowerInvariant()

  return @{
    "X-Probe-Node-Id" = $safeNodeID
    "X-Probe-Timestamp" = $timestamp
    "X-Probe-Rand" = $randomToken
    "X-Probe-Signature" = $signature
  }
}

function Build-ProbeProxyURL {
  param(
    [string]$Endpoint,
    [string]$ExtraQuery
  )

  $proxyBase = if ($env:PROBE_PROXY_BASE_URL) { $env:PROBE_PROXY_BASE_URL.Trim() } else { "" }
  $nodeID = if ($env:PROBE_NODE_ID) { $env:PROBE_NODE_ID.Trim() } else { "" }
  $nodeSecret = if ($env:PROBE_NODE_SECRET) { $env:PROBE_NODE_SECRET.Trim() } else { "" }
  if (-not $proxyBase -or -not $nodeID -or -not $nodeSecret -or -not $Endpoint) {
    return ""
  }

  $url = $proxyBase.TrimEnd("/") + "/" + $Endpoint + "?node_id=" + [Uri]::EscapeDataString($nodeID) + "&secret=" + [Uri]::EscapeDataString($nodeSecret)
  if ($ExtraQuery) {
    $url += "&" + $ExtraQuery
  }
  return $url
}

function Get-AssetDownloadURL {
  param([object]$Asset)
  if (-not $Asset) {
    return ""
  }
  foreach ($name in @("browser_download_url", "download_url", "browserDownloadUrl", "downloadUrl", "BrowserDownloadURL", "DownloadURL")) {
    $prop = $Asset.PSObject.Properties[$name]
    if ($prop -and $prop.Value) {
      $v = ([string]$prop.Value).Trim()
      if ($v) {
        return $v
      }
    }
  }
  return ""
}

function Invoke-DownloadFile {
  param(
    [string]$Url,
    [string]$OutFile
  )
  $proxyURLFromPath = Build-ProbeProxyURL -Endpoint "download" -ExtraQuery ("url=" + [Uri]::EscapeDataString($Url))
  if ($proxyURLFromPath) {
    $proxyHeaders = @{
      "User-Agent" = "cloudhelper-probe-node-install"
      "Accept" = "application/octet-stream"
    }
    Write-Log "downloading via proxy path"
    try {
      Invoke-WebRequest -UseBasicParsing -Uri $proxyURLFromPath -Headers $proxyHeaders -OutFile $OutFile -TimeoutSec 300
      return
    } catch {
      throw "[cloudhelper-probe-node-install][ERROR] download failed via proxy path: $($_.Exception.Message)"
    }
  }

  $headers = @{
    "User-Agent" = "cloudhelper-probe-node-install"
  }
  if ($env:GITHUB_TOKEN) {
    $headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
  }
  try {
    Invoke-WebRequest -UseBasicParsing -Uri $Url -Headers $headers -OutFile $OutFile -TimeoutSec 300
    return
  } catch {
    $directError = $_
  }

  $controllerURL = if ($env:PROBE_CONTROLLER_URL) { $env:PROBE_CONTROLLER_URL.Trim() } else { "" }
  $nodeID = if ($env:PROBE_NODE_ID) { $env:PROBE_NODE_ID.Trim() } else { "" }
  $nodeSecret = if ($env:PROBE_NODE_SECRET) { $env:PROBE_NODE_SECRET.Trim() } else { "" }
  $canProxy = $controllerURL -and $nodeID -and $nodeSecret -and $Url.ToLowerInvariant().StartsWith("https://")
  if (-not $canProxy) {
    throw $directError
  }

  $proxyHeaders = New-ProbeAuthHeaders -NodeID $nodeID -NodeSecret $nodeSecret
  if (-not $proxyHeaders) {
    throw $directError
  }
  $proxyHeaders["User-Agent"] = "cloudhelper-probe-node-install"
  $proxyHeaders["Accept"] = "application/octet-stream"

  $proxyURL = $controllerURL.TrimEnd("/") + "/api/probe/proxy/download?url=" + [Uri]::EscapeDataString($Url)
  Write-Log "direct download failed, retrying via controller proxy"

  try {
    Invoke-WebRequest -UseBasicParsing -Uri $proxyURL -Headers $proxyHeaders -OutFile $OutFile -TimeoutSec 300
    return
  } catch {
    $proxyError = $_
    throw "[cloudhelper-probe-node-install][ERROR] download failed (direct: $($directError.Exception.Message); proxy: $($proxyError.Exception.Message))"
  }
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

function New-LegacySuffix {
  return "legacy.$([DateTime]::UtcNow.ToString("yyyyMMddHHmmss")).$(New-RandomHexToken -ByteLength 2)"
}

function Move-FileWithConflictHandling {
  param(
    [string]$SourcePath,
    [string]$DestinationPath,
    [string]$Label
  )

  if (-not (Test-Path -LiteralPath $SourcePath)) {
    return
  }

  $targetDir = Split-Path -Path $DestinationPath -Parent
  if ($targetDir -and -not (Test-Path -LiteralPath $targetDir)) {
    New-Item -ItemType Directory -Path $targetDir -Force | Out-Null
  }

  if (Test-Path -LiteralPath $DestinationPath) {
    $legacyDestination = "$DestinationPath.$(New-LegacySuffix)"
    Write-Log "$Label destination already exists, moving legacy source to: $legacyDestination"
    Move-Item -LiteralPath $SourcePath -Destination $legacyDestination -Force
    return
  }

  Move-Item -LiteralPath $SourcePath -Destination $DestinationPath -Force
}

function Move-DirectoryContentWithConflictHandling {
  param(
    [string]$SourceDir,
    [string]$DestinationDir,
    [string]$Label
  )

  if (-not (Test-Path -LiteralPath $SourceDir)) {
    return
  }

  if (-not (Test-Path -LiteralPath $DestinationDir)) {
    New-Item -ItemType Directory -Path $DestinationDir -Force | Out-Null
  }

  $entries = Get-ChildItem -LiteralPath $SourceDir -Force -ErrorAction SilentlyContinue
  foreach ($entry in $entries) {
    $targetPath = Join-Path $DestinationDir $entry.Name
    if ($entry.PSIsContainer) {
      if (Test-Path -LiteralPath $targetPath) {
        $targetItem = Get-Item -LiteralPath $targetPath -Force
        if ($targetItem.PSIsContainer) {
          Move-DirectoryContentWithConflictHandling -SourceDir $entry.FullName -DestinationDir $targetPath -Label $Label
          if (Test-Path -LiteralPath $entry.FullName) {
            $remaining = @(Get-ChildItem -LiteralPath $entry.FullName -Force -ErrorAction SilentlyContinue)
            if ($remaining.Count -eq 0) {
              Remove-Item -LiteralPath $entry.FullName -Force -ErrorAction SilentlyContinue
            }
          }
        } else {
          $legacyTargetPath = "$targetPath.$(New-LegacySuffix)"
          Write-Log "$Label conflict detected, moving source directory to: $legacyTargetPath"
          Move-Item -LiteralPath $entry.FullName -Destination $legacyTargetPath -Force
        }
      } else {
        Move-Item -LiteralPath $entry.FullName -Destination $targetPath -Force
      }
      continue
    }

    Move-FileWithConflictHandling -SourcePath $entry.FullName -DestinationPath $targetPath -Label $Label
  }

  if (Test-Path -LiteralPath $SourceDir) {
    $remaining = @(Get-ChildItem -LiteralPath $SourceDir -Force -ErrorAction SilentlyContinue)
    if ($remaining.Count -eq 0) {
      Remove-Item -LiteralPath $SourceDir -Force -ErrorAction SilentlyContinue
    }
  }
}

function Move-LegacyProbeLayoutToRuntimeDir {
  param(
    [string]$InstallDir,
    [string]$RuntimeDir,
    [string]$ServiceName
  )

  $legacyProbeExePath = Join-Path $InstallDir "probe_node.exe"
  $legacyDataDir = Join-Path $InstallDir "data"
  $legacyLogsDir = Join-Path $InstallDir "logs"
  $legacyWinSWExePath = Join-Path $InstallDir "$ServiceName-service.exe"
  $legacyWinSWXmlPath = Join-Path $InstallDir "$ServiceName-service.xml"

  Move-FileWithConflictHandling -SourcePath $legacyProbeExePath -DestinationPath (Join-Path $RuntimeDir "probe_node.exe") -Label "legacy binary"
  Move-FileWithConflictHandling -SourcePath $legacyWinSWExePath -DestinationPath (Join-Path $RuntimeDir "$ServiceName-service.exe") -Label "legacy WinSW executable"
  Move-FileWithConflictHandling -SourcePath $legacyWinSWXmlPath -DestinationPath (Join-Path $RuntimeDir "$ServiceName-service.xml") -Label "legacy WinSW xml"

  $legacyBackups = Get-ChildItem -LiteralPath $InstallDir -Filter "probe_node.exe.bak*" -File -ErrorAction SilentlyContinue
  foreach ($backup in $legacyBackups) {
    Move-FileWithConflictHandling -SourcePath $backup.FullName -DestinationPath (Join-Path $RuntimeDir $backup.Name) -Label "legacy binary backup"
  }

  Move-DirectoryContentWithConflictHandling -SourceDir $legacyDataDir -DestinationDir (Join-Path $RuntimeDir "data") -Label "legacy data"
  Move-DirectoryContentWithConflictHandling -SourceDir $legacyLogsDir -DestinationDir (Join-Path $RuntimeDir "logs") -Label "legacy logs"
}

function Invoke-ServiceUninstallBestEffort {
  param(
    [string]$ServiceName,
    [string[]]$WinSWCandidates
  )

  foreach ($candidate in $WinSWCandidates) {
    $path = if ($candidate) { $candidate.Trim() } else { "" }
    if (-not $path) {
      continue
    }
    if (-not (Test-Path -LiteralPath $path)) {
      continue
    }

    try {
      Write-Log "attempting service uninstall via: $path"
      & $path uninstall | Out-Null
    } catch {
      Write-Log "warning: WinSW uninstall failed via ${path}: $($_.Exception.Message)"
    }
  }

  if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    try {
      & sc.exe delete $ServiceName | Out-Null
    } catch {
      Write-Log "warning: sc delete failed for ${ServiceName}: $($_.Exception.Message)"
    }
  }
}

Require-Admin

$releaseRepo = if ($env:RELEASE_REPO) { $env:RELEASE_REPO.Trim() } else { "fengzhanhuaer/CloudHelper" }
$releaseTag = if ($env:RELEASE_TAG) { $env:RELEASE_TAG.Trim() } else { "latest" }
$assetNameOverride = if ($env:ASSET_NAME) { $env:ASSET_NAME.Trim() } else { "" }

$installDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR.Trim() } else { "C:\Tools" }
$runtimeDir = Join-Path $installDir "probe_node"
$serviceName = if ($env:SERVICE_NAME) { $env:SERVICE_NAME.Trim() } else { "probe_node" }
$serviceDisplayName = if ($env:SERVICE_DISPLAY_NAME) { $env:SERVICE_DISPLAY_NAME.Trim() } else { "CloudHelper Probe Node" }
$winswVersion = if ($env:WINSW_VERSION) { $env:WINSW_VERSION.Trim() } else { "v2.12.0" }

$nodeID = if ($env:PROBE_NODE_ID) { $env:PROBE_NODE_ID.Trim() } else { "" }
$nodeSecret = if ($env:PROBE_NODE_SECRET) { $env:PROBE_NODE_SECRET.Trim() } else { "" }
$controllerURL = if ($env:PROBE_CONTROLLER_URL) { $env:PROBE_CONTROLLER_URL.Trim() } else { "" }
$listenAddr = if ($env:PROBE_NODE_LISTEN) { $env:PROBE_NODE_LISTEN.Trim() } else { "" }

$runtimeLogsDir = Join-Path $runtimeDir "logs"
$runtimeDataDir = Join-Path $runtimeDir "data"

if (-not (Test-Path -LiteralPath $installDir)) {
  Write-Log "creating install directory: $installDir"
  New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $runtimeDir)) {
  Write-Log "creating runtime directory: $runtimeDir"
  New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $runtimeLogsDir)) {
  New-Item -ItemType Directory -Path $runtimeLogsDir -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $runtimeDataDir)) {
  New-Item -ItemType Directory -Path $runtimeDataDir -Force | Out-Null
}

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cloudhelper-probe-node-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
try {
  $releaseAPI = if ($env:PROBE_RELEASE_API_URL) {
    $env:PROBE_RELEASE_API_URL.Trim()
  } elseif ($releaseTag -eq "latest") {
    $proxyReleaseAPI = Build-ProbeProxyURL -Endpoint "github/latest" -ExtraQuery ("repo=" + [Uri]::EscapeDataString($releaseRepo))
    if ($proxyReleaseAPI) {
      $proxyReleaseAPI
    } else {
      "https://api.github.com/repos/$releaseRepo/releases/latest"
    }
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
  $assetURL = Get-AssetDownloadURL -Asset $asset
  if (-not $assetURL) {
    Fail "selected asset has empty download url"
  }

  $assetFile = Join-Path $tmpDir $assetName
  Write-Log "downloading probe asset: $assetName"
  Invoke-DownloadFile -Url $assetURL -OutFile $assetFile

  $winswExePath = Join-Path $runtimeDir "$serviceName-service.exe"
  $winswXmlPath = Join-Path $runtimeDir "$serviceName-service.xml"
  $legacyWinswExePath = Join-Path $installDir "$serviceName-service.exe"

  $existingService = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
  if ($existingService) {
    Write-Log "service exists, stopping: $serviceName"
    try { & sc.exe stop $serviceName | Out-Null } catch {}
    Start-Sleep -Seconds 2
  }

  Write-Log "migrating legacy install layout to runtime directory: $runtimeDir"
  Move-LegacyProbeLayoutToRuntimeDir -InstallDir $installDir -RuntimeDir $runtimeDir -ServiceName $serviceName

  $probeExePath = Join-Path $runtimeDir "probe_node.exe"
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
    Invoke-ServiceUninstallBestEffort -ServiceName $serviceName -WinSWCandidates @($winswExePath, $legacyWinswExePath)
    Start-Sleep -Seconds 1
  }

  Write-Log "installing windows service: $serviceName"
  & $winswExePath install | Out-Null
  & sc.exe config $serviceName start= auto | Out-Null
  & $winswExePath start | Out-Null

  Write-Log "installed successfully"
  Write-Log "service: $serviceName"
  Write-Log "install root: $installDir"
  Write-Log "runtime dir: $runtimeDir"
  Write-Log "binary: $probeExePath"
  Write-Log "release: $tagName"
  Write-Log "asset: $assetName"
  Write-Log "check status: sc query $serviceName"
} finally {
  Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
