param(
  [string]$ServiceName = $(if ($env:SERVICE_NAME) { $env:SERVICE_NAME.Trim() } else { "probe_node" }),
  [int]$StopTimeoutSec = 30,
  [int]$StartTimeoutSec = 30
)

$ErrorActionPreference = "Stop"

function Write-Log {
  param([string]$Message)
  Write-Host ("[{0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $Message)
}

function Fail {
  param([string]$Message)
  Write-Error $Message
  exit 1
}

function Test-IsAdmin {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($identity)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Wait-ServiceStatus {
  param(
    [string]$Name,
    [string]$Status,
    [int]$TimeoutSec
  )
  $deadline = (Get-Date).AddSeconds([Math]::Max(1, $TimeoutSec))
  while ((Get-Date) -lt $deadline) {
    $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
    if ($null -eq $svc) {
      return $false
    }
    if ([string]$svc.Status -eq $Status) {
      return $true
    }
    Start-Sleep -Milliseconds 500
  }
  return $false
}

if (-not (Test-IsAdmin)) {
  Fail "请以管理员身份运行 PowerShell 后重试。"
}

$ServiceName = $ServiceName.Trim()
if ([string]::IsNullOrWhiteSpace($ServiceName)) {
  Fail "ServiceName 不能为空。"
}

$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -eq $service) {
  Fail "未找到 Windows 服务：$ServiceName"
}

Write-Log "restarting service: $ServiceName"

if ($service.Status -ne "Stopped") {
  Write-Log "stopping service: current=$($service.Status)"
  try {
    Stop-Service -Name $ServiceName -Force -ErrorAction Stop
  } catch {
    Write-Log "Stop-Service failed, fallback to sc.exe stop: $($_.Exception.Message)"
    & sc.exe stop $ServiceName | Out-Null
  }
  if (-not (Wait-ServiceStatus -Name $ServiceName -Status "Stopped" -TimeoutSec $StopTimeoutSec)) {
    $latest = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    $status = if ($latest) { [string]$latest.Status } else { "missing" }
    Fail "服务停止超时：$ServiceName status=$status"
  }
}

Write-Log "starting service"
try {
  Start-Service -Name $ServiceName -ErrorAction Stop
} catch {
  Write-Log "Start-Service failed, fallback to sc.exe start: $($_.Exception.Message)"
  & sc.exe start $ServiceName | Out-Null
}

if (-not (Wait-ServiceStatus -Name $ServiceName -Status "Running" -TimeoutSec $StartTimeoutSec)) {
  $latest = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  $status = if ($latest) { [string]$latest.Status } else { "missing" }
  Fail "服务启动超时：$ServiceName status=$status"
}

Write-Log "service restarted: $ServiceName"
