<#
.SYNOPSIS
Pull --rebase, run key probe_node tests, and push through the local system proxy.

.EXAMPLE
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\push_with_system_proxy.ps1

.EXAMPLE
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\push_with_system_proxy.ps1 -Branch mapledev -SkipTests
#>

param(
  [string]$Remote = "origin",
  [string]$Branch = "",
  [string]$Proxy = "http://127.0.0.1:18080",
  [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

function Invoke-Step {
  param(
    [string]$Title,
    [scriptblock]$Action
  )
  Write-Host ""
  Write-Host "==> $Title"
  & $Action
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Resolve-Path (Join-Path $scriptDir "..")
Set-Location $repoRoot

$currentBranch = (git rev-parse --abbrev-ref HEAD).Trim()
if ($currentBranch -eq "HEAD") {
  throw "Detached HEAD is not supported. Checkout a branch first."
}
if ([string]::IsNullOrWhiteSpace($Branch)) {
  $Branch = $currentBranch
}

$dirty = (git status --porcelain)
if ($dirty) {
  git status --short
  throw "Working tree is not clean. Commit or stash changes before pushing."
}

$gitProxyArgs = @(
  "-c", "http.proxy=$Proxy",
  "-c", "https.proxy=$Proxy",
  "-c", "http.sslBackend=openssl",
  "-c", "http.version=HTTP/1.1"
)

Write-Host "Repo:   $repoRoot"
Write-Host "Remote: $Remote"
Write-Host "Branch: $Branch"
Write-Host "Proxy:  $Proxy"

Invoke-Step "Fetch and rebase $Remote/$Branch" {
  git @gitProxyArgs pull --rebase $Remote $Branch
}

if (-not $SkipTests) {
  $probeNodeDir = Join-Path $repoRoot "probe_node"
  if (Test-Path $probeNodeDir) {
    Invoke-Step "Run probe_node smoke tests" {
      Push-Location $probeNodeDir
      try {
        go test -run "TestProbeLocalVirtualRouterRouteTestHandlerReturnsCurlResult|TestProbeLocalAPIMethodGuards|TestProbeVirtualRouterTUNPacketDropsFakeIPWhenExitCarrierUnavailable" .
      } finally {
        Pop-Location
      }
    }
  } else {
    Write-Host "Skip tests: probe_node directory not found."
  }
}

Invoke-Step "Push $Branch to $Remote" {
  git @gitProxyArgs push $Remote $Branch
}

Invoke-Step "Final status" {
  git status --short --branch
  git log -1 --oneline --decorate
}
