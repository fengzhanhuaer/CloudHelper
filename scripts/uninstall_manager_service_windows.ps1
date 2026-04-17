param(
  [string]$InstallRoot = 'C:\Tools\CloudManager\',
  [string]$ServiceName = "CloudManagerService",
  [switch]$PurgeInstallDir
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Log {
  param([string]$Message)
  Write-Host "[manager-service-uninstall] $Message"
}

function Fail {
  param([string]$Message)
  throw "[manager-service-uninstall][ERROR] $Message"
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
  param([string[]]$ScArgs)
  & sc.exe @ScArgs | Out-Host
  if ($LASTEXITCODE -ne 0) {
    throw "sc.exe failed: $($ScArgs -join ' ')"
  }
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

Require-Admin

$installDir = [System.IO.Path]::GetFullPath($InstallRoot)
Write-Log "service name: $ServiceName"
Write-Log "install root: $installDir"

if (Get-ServiceExists -Name $ServiceName) {
  try {
    Exec-Sc -ScArgs @("stop", $ServiceName)
  } catch {
    Write-Log "stop service ignored: $($_.Exception.Message)"
  }

  try {
    Exec-Sc -ScArgs @("delete", $ServiceName)
  } catch {
    Fail "delete service failed: $($_.Exception.Message)"
  }

  Wait-ServiceDeleted -Name $ServiceName
  Write-Log "service removed"
} else {
  Write-Log "service not found, skip delete"
}

if ($PurgeInstallDir) {
  if (Test-Path -LiteralPath $installDir) {
    Remove-Item -LiteralPath $installDir -Recurse -Force
    Write-Log "install directory purged"
  } else {
    Write-Log "install directory not found, skip purge"
  }
}

Write-Log "uninstall completed"
