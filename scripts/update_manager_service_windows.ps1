param(
  [string]$InstallRoot = 'C:\Tools\CloudManager\',
  [string]$ServiceName = "CloudManagerService",
  [string]$NewBinaryPath = "",
  [string]$GitHubRepo = "fengzhanhuaer/CloudHelper",
  [string]$AssetName = "cloudhelper-manager-service-windows-amd64.exe",
  [string]$Version = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Log {
  param([string]$Message)
  Write-Host "[manager-service-update] $Message"
}

function Fail {
  param([string]$Message)
  throw "[manager-service-update][ERROR] $Message"
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

function Exec-Sc {
  param([string[]]$Args)
  & sc.exe @Args | Out-Host
  if ($LASTEXITCODE -ne 0) {
    Fail "sc.exe failed: $($Args -join ' ')"
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
    "User-Agent" = "cloudhelper-manager-service-update"
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
    "User-Agent" = "cloudhelper-manager-service-update"
    "Accept" = "application/octet-stream"
  }
  if ($env:GITHUB_TOKEN) {
    $headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
  }
  Invoke-WebRequest -UseBasicParsing -Uri $Url -Headers $headers -OutFile $OutFile -TimeoutSec 300
}

function Resolve-NewBinaryPath {
  param(
    [string]$Requested,
    [string]$Repo,
    [string]$Asset,
    [string]$InputVersion,
    [string]$TempDir
  )

  if ($Requested -and (Test-Path -LiteralPath $Requested)) {
    Write-Log "using local binary from -NewBinaryPath"
    return (Resolve-Path -LiteralPath $Requested).Path
  }

  $downloadURL = Resolve-ReleaseAssetDownloadURL -Repo $Repo -Asset $Asset -InputVersion $InputVersion
  $target = Join-Path $TempDir $Asset
  Write-Log "downloading asset: $Asset"
  Invoke-DownloadFile -Url $downloadURL -OutFile $target
  return $target
}

Require-Admin

if (-not (Get-ServiceExists -Name $ServiceName)) {
  Fail "service '$ServiceName' does not exist"
}

$installDir = [System.IO.Path]::GetFullPath($InstallRoot)
$targetExe = Join-Path $installDir "manager_service.exe"
$backupExe = Join-Path $installDir "manager_service.exe.bak"
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cloudmanager-update-" + [guid]::NewGuid().ToString("N"))

if (-not (Test-Path -LiteralPath $targetExe)) {
  Fail "installed binary not found: $targetExe"
}

Write-Log "service name: $ServiceName"
Write-Log "install root: $installDir"
Write-Log "github repo: $GitHubRepo"
Write-Log "asset name: $AssetName"
if ($Version) {
  Write-Log "version override: $(Get-NormalizedTag -InputVersion $Version)"
} else {
  Write-Log "version override: <latest>"
}

New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

try {
  $newExe = Resolve-NewBinaryPath -Requested $NewBinaryPath -Repo $GitHubRepo -Asset $AssetName -InputVersion $Version -TempDir $tempDir
  Write-Log "new binary: $newExe"

  try {
    Exec-Sc -Args @("stop", $ServiceName)
  } catch {
    Write-Log "stop service ignored: $($_.Exception.Message)"
  }

  if (Test-Path -LiteralPath $backupExe) {
    Remove-Item -LiteralPath $backupExe -Force
  }

  Copy-Item -LiteralPath $targetExe -Destination $backupExe -Force
  Copy-Item -LiteralPath $newExe -Destination $targetExe -Force
  Write-Log "binary replaced"

  try {
    Exec-Sc -Args @("start", $ServiceName)
    $svc = Get-Service -Name $ServiceName -ErrorAction Stop
    Write-Log "service status: $($svc.Status)"
    Write-Log "update completed"
  } catch {
    Write-Log "start failed, rolling back binary..."
    Copy-Item -LiteralPath $backupExe -Destination $targetExe -Force
    try {
      Exec-Sc -Args @("start", $ServiceName)
    } catch {
      Write-Log "rollback start failed: $($_.Exception.Message)"
    }
    Fail "update failed and rollback executed: $($_.Exception.Message)"
  }
}
finally {
  if (Test-Path -LiteralPath $tempDir) {
    Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
