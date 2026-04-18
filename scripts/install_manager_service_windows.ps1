param(
  [string]$InstallRoot = 'C:\Tools\CloudManager\',
  [string]$BinaryPath = "",
  [string]$ServiceName = "CloudManagerService",
  [string]$ServiceDisplayName = "CloudManager Service",
  [string]$ServiceDescription = "CloudHelper manager_service backend service",
  [string]$ControllerURL = "http://127.0.0.1:15030",
  [string]$GitHubRepo = "fengzhanhuaer/CloudHelper",
  [string]$AssetName = "cloudhelper-manager-service-windows-amd64.exe",
  [string]$Version = "",
  [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Log {
  param([string]$Message)
  Write-Host "[manager-service-install] $Message"
}

function Fail {
  param([string]$Message)
  throw "[manager-service-install][ERROR] $Message"
}

function Require-Admin {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($identity)
  if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail "please run powershell as administrator"
  }
}

function Get-ServiceExists {
  param([string]$Name)
  return $null -ne (Get-Service -Name $Name -ErrorAction SilentlyContinue)
}

function Wait-ServiceDeleted {
  param([string]$Name)
  for ($i = 0; $i -lt 30; $i++) {
    if (-not (Get-ServiceExists -Name $Name)) {
      return
    }
    Start-Sleep -Milliseconds 300
  }
  Fail "service '$Name' still exists after delete"
}

function Exec-Sc {
  param([string[]]$ScArgs)
  & sc.exe @ScArgs | Out-Host
  if ($LASTEXITCODE -ne 0) {
    Fail "sc.exe failed: $($ScArgs -join ' ')"
  }
}

function Get-NormalizedTag {
  param([string]$InputVersion)
  $v = ""
  if ($InputVersion) {
    $v = $InputVersion.Trim()
  }
  if (-not $v) {
    return ""
  }
  if ($v.StartsWith("v")) {
    return $v
  }
  return "v$v"
}

function Invoke-GitHubApiJson {
  param([string]$Url)

  $headers = @{
    "Accept" = "application/vnd.github+json"
    "User-Agent" = "cloudhelper-manager-service-install"
  }
  if ($env:GITHUB_TOKEN) {
    $headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
  }

  return Invoke-RestMethod -Method Get -Uri $Url -Headers $headers -TimeoutSec 60
}

function Resolve-ReleaseAssetDownloadURL {
  param(
    [string]$Repo,
    [string]$Asset,
    [string]$InputVersion
  )

  if (-not $Repo) {
    Fail "GitHub repo is required"
  }
  if (-not $Asset) {
    Fail "asset name is required"
  }

  $tag = Get-NormalizedTag -InputVersion $InputVersion
  $api = ""
  if ($tag) {
    $api = "https://api.github.com/repos/$Repo/releases/tags/$tag"
    Write-Log "fetching GitHub release by tag: $tag"
  } else {
    $api = "https://api.github.com/repos/$Repo/releases/latest"
    Write-Log "fetching GitHub latest release"
  }

  $release = Invoke-GitHubApiJson -Url $api
  if (-not $release -or -not $release.assets) {
    Fail "release assets are empty from $api"
  }

  foreach ($item in $release.assets) {
    if ([string]$item.name -eq $Asset) {
      $url = [string]$item.browser_download_url
      if (-not $url) {
        Fail "asset '$Asset' has empty browser_download_url"
      }
      return $url
    }
  }

  $names = @($release.assets | ForEach-Object { $_.name }) -join ", "
  Fail "asset '$Asset' not found in release assets: [$names]"
}

function Invoke-DownloadFile {
  param(
    [string]$Url,
    [string]$OutFile
  )

  $headers = @{
    "User-Agent" = "cloudhelper-manager-service-install"
    "Accept" = "application/octet-stream"
  }
  if ($env:GITHUB_TOKEN) {
    $headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
  }

  Invoke-WebRequest -UseBasicParsing -Uri $Url -Headers $headers -OutFile $OutFile -TimeoutSec 300
}

function Resolve-SourceBinary {
  param(
    [string]$Requested,
    [string]$Repo,
    [string]$Asset,
    [string]$InputVersion,
    [string]$TempDir
  )

  if ($Requested -and (Test-Path -LiteralPath $Requested)) {
    Write-Log "using local binary from -BinaryPath"
    return (Resolve-Path -LiteralPath $Requested).Path
  }

  $downloadURL = Resolve-ReleaseAssetDownloadURL -Repo $Repo -Asset $Asset -InputVersion $InputVersion
  $target = Join-Path $TempDir $Asset
  Write-Log "downloading asset: $Asset"
  Invoke-DownloadFile -Url $downloadURL -OutFile $target
  return $target
}

function Ensure-ConfigFile {
  param(
    [string]$DataDir,
    [string]$Controller
  )

  $cfgPath = Join-Path $DataDir "manager_service_config.json"
  if (Test-Path -LiteralPath $cfgPath) {
    Write-Log "config exists, keep unchanged: $cfgPath"
    return
  }

  $cfg = [ordered]@{
    listen_addr    = "127.0.0.1:16033"
    controller_url = $Controller
  }
  $raw = ($cfg | ConvertTo-Json -Depth 4)
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($cfgPath, ($raw + "`r`n"), $utf8NoBom)
  Write-Log "config created: $cfgPath"
}

Require-Admin

$installDir = [System.IO.Path]::GetFullPath($InstallRoot)
$targetExe = Join-Path $installDir "manager_service.exe"
$dataDir = Join-Path $installDir "data"
$logDir = Join-Path $installDir "log"
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cloudmanager-install-" + [guid]::NewGuid().ToString("N"))

Write-Log "install root: $installDir"
Write-Log "service name: $ServiceName"
Write-Log "github repo: $GitHubRepo"
Write-Log "asset name: $AssetName"
if ($Version) {
  Write-Log "version override: $(Get-NormalizedTag -InputVersion $Version)"
} else {
  Write-Log "version override: <latest>"
}

New-Item -ItemType Directory -Path $installDir -Force | Out-Null
New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
New-Item -ItemType Directory -Path $logDir -Force | Out-Null
New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

try {
  $sourceExe = Resolve-SourceBinary -Requested $BinaryPath -Repo $GitHubRepo -Asset $AssetName -InputVersion $Version -TempDir $tempDir
  Write-Log "source exe: $sourceExe"

  if (Get-ServiceExists -Name $ServiceName) {
    if ($Force) {
      Write-Log "-Force detected; compatibility mode (reinstall is now default)"
    }

    Write-Log "existing service detected, uninstalling and reinstalling..."
    try {
      Exec-Sc -ScArgs @("stop", $ServiceName)
    } catch {
      Write-Log "stop existing service ignored: $($_.Exception.Message)"
    }

    try {
      Exec-Sc -ScArgs @("delete", $ServiceName)
    } catch {
      Write-Log "delete existing service ignored: $($_.Exception.Message)"
    }
    Wait-ServiceDeleted -Name $ServiceName
  }

  Copy-Item -LiteralPath $sourceExe -Destination $targetExe -Force
  Write-Log "binary copied: $targetExe"

  $binPath = '"' + $targetExe + '"'
  Exec-Sc -ScArgs @("create", $ServiceName, "binPath=", $binPath, "start=", "auto", "DisplayName=", $ServiceDisplayName)
  Exec-Sc -ScArgs @("description", $ServiceName, $ServiceDescription)

  Ensure-ConfigFile -DataDir $dataDir -Controller $ControllerURL

  Exec-Sc -ScArgs @("start", $ServiceName)
  $svc = Get-Service -Name $ServiceName -ErrorAction Stop
  Write-Log "service status: $($svc.Status)"
  Write-Log "install completed"
}
finally {
  if (Test-Path -LiteralPath $tempDir) {
    Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
