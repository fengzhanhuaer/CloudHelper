param(
  [string]$ServiceName = $(if ($env:SERVICE_NAME) { $env:SERVICE_NAME.Trim() } else { "probe_node" }),
  [int]$StopTimeoutSec = 30,
  [int]$StartTimeoutSec = 30,
  [switch]$NoWinSW,
  [switch]$KeepOrphanProcess,
  [switch]$StopOnly
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

function Get-ServiceConfig {
  param([string]$Name)
  return Get-CimInstance -ClassName Win32_Service -Filter ("Name='{0}'" -f $Name.Replace("'", "''")) -ErrorAction SilentlyContinue
}

function Resolve-WinSWPath {
  param([string]$Name)
  $candidates = @()
  if ($PSScriptRoot) {
    $candidates += (Join-Path $PSScriptRoot "$Name-service.exe")
  }
  $svcCfg = Get-ServiceConfig -Name $Name
  if ($svcCfg -and $svcCfg.PathName) {
    $raw = ([string]$svcCfg.PathName).Trim()
    if ($raw.StartsWith('"')) {
      $end = $raw.IndexOf('"', 1)
      if ($end -gt 1) {
        $candidates += $raw.Substring(1, $end - 1)
      }
    } else {
      $candidates += ($raw -split '\s+', 2)[0]
    }
  }
  $candidates += (Join-Path "C:\Tools\probe_node" "$Name-service.exe")
  foreach ($candidate in $candidates) {
    $path = if ($candidate) { ([string]$candidate).Trim() } else { "" }
    if ($path -and (Test-Path -LiteralPath $path)) {
      return (Resolve-Path -LiteralPath $path).Path
    }
  }
  return ""
}

function Resolve-ProbeExePath {
  param([string]$WinSWPath)
  if (-not $WinSWPath) {
    return ""
  }
  $baseDir = Split-Path -Path $WinSWPath -Parent
  $xmlPath = [System.IO.Path]::ChangeExtension($WinSWPath, ".xml")
  if (Test-Path -LiteralPath $xmlPath) {
    try {
      [xml]$xml = Get-Content -LiteralPath $xmlPath -Raw
      $exeText = ([string]$xml.service.executable).Trim()
      if ($exeText) {
        $exeText = $exeText.Replace("%BASE%", $baseDir)
        if (-not [System.IO.Path]::IsPathRooted($exeText)) {
          $exeText = Join-Path $baseDir $exeText
        }
        if (Test-Path -LiteralPath $exeText) {
          return (Resolve-Path -LiteralPath $exeText).Path
        }
      }
    } catch {
      Write-Log "warning: parse WinSW xml failed: $($_.Exception.Message)"
    }
  }
  $fallback = Join-Path $baseDir "probe_node.exe"
  if (Test-Path -LiteralPath $fallback) {
    return (Resolve-Path -LiteralPath $fallback).Path
  }
  return ""
}

function Stop-OrphanProbeNodeProcesses {
  param(
    [string]$ProbeExePath,
    [string]$ServiceName
  )
  if (-not $ProbeExePath) {
    return
  }
  $resolvedProbeExePath = (Resolve-Path -LiteralPath $ProbeExePath).Path
  $currentPID = $PID
  $processes = Get-CimInstance Win32_Process -Filter "Name='probe_node.exe'" -ErrorAction SilentlyContinue |
    Where-Object {
      $_.ProcessId -ne $currentPID -and
      $_.ExecutablePath -and
      ([string]$_.ExecutablePath).Trim().Equals($resolvedProbeExePath, [StringComparison]::OrdinalIgnoreCase)
    }
  foreach ($proc in $processes) {
    Write-Log "terminating orphan probe_node process: pid=$($proc.ProcessId) path=$($proc.ExecutablePath)"
    try {
      Stop-Process -Id $proc.ProcessId -Force -ErrorAction Stop
    } catch {
      Write-Log "warning: failed to terminate orphan process pid=$($proc.ProcessId): $($_.Exception.Message)"
    }
  }
  if ($processes) {
    Start-Sleep -Milliseconds 800
  }
}

function Invoke-NativeCommand {
  param(
    [string]$FilePath,
    [string[]]$Arguments
  )
  $output = & $FilePath @Arguments 2>&1
  $code = $LASTEXITCODE
  if ($output) {
    $output | ForEach-Object { Write-Log ([string]$_) }
  }
  return $code
}

function Write-ServiceDiagnostics {
  param(
    [string]$Name,
    [string]$WinSWPath
  )
  Write-Log "diagnostics begin"
  $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
  if ($svc) {
    Write-Log "service status: $($svc.Status)"
  }
  $svcCfg = Get-ServiceConfig -Name $Name
  if ($svcCfg) {
    Write-Log "service pathname: $($svcCfg.PathName)"
    Write-Log "service startname: $($svcCfg.StartName)"
    Write-Log "service state: $($svcCfg.State)"
    if ($svcCfg.ExitCode -ne 0 -or $svcCfg.ServiceSpecificExitCode -ne 0) {
      Write-Log "service exit_code=$($svcCfg.ExitCode) service_specific_exit_code=$($svcCfg.ServiceSpecificExitCode)"
    }
  }
  Write-Log "sc queryex:"
  & sc.exe queryex $Name 2>&1 | ForEach-Object { Write-Log ([string]$_) }
  Write-Log "sc qc:"
  & sc.exe qc $Name 2>&1 | ForEach-Object { Write-Log ([string]$_) }
  if ($WinSWPath) {
    Write-Log "winsw path: $WinSWPath"
    Write-Log "winsw status:"
    & $WinSWPath status 2>&1 | ForEach-Object { Write-Log ([string]$_) }
    $baseDir = Split-Path -Path $WinSWPath -Parent
    $logDir = Join-Path $baseDir "logs"
    if (Test-Path -LiteralPath $logDir) {
      $priorityLogs = Get-ChildItem -LiteralPath $logDir -File -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match '\.(out|err)\.log$' -or $_.Name -match 'runtime|stdout|stderr|wrapper' } |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 8
      $recentLogs = Get-ChildItem -LiteralPath $logDir -File -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 6
      $logs = @($priorityLogs + $recentLogs) |
        Sort-Object FullName -Unique |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 10
      foreach ($log in $logs) {
        Write-Log "log tail: $($log.FullName)"
        Get-Content -LiteralPath $log.FullName -Tail 40 -ErrorAction SilentlyContinue |
          ForEach-Object { Write-Host $_ }
      }
    }
  }
  Write-Log "diagnostics end"
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

$winswPath = ""
if (-not $NoWinSW) {
  $winswPath = Resolve-WinSWPath -Name $ServiceName
}
if ($winswPath) {
  Write-Log "using WinSW wrapper: $winswPath"
} else {
  Write-Log "WinSW wrapper not found, using Windows service control"
}
$probeExePath = Resolve-ProbeExePath -WinSWPath $winswPath
if ($probeExePath) {
  Write-Log "probe executable: $probeExePath"
}

Write-Log "restarting service: $ServiceName"

if ($service.Status -ne "Stopped") {
  Write-Log "stopping service: current=$($service.Status)"
  if ($winswPath) {
    $stopCode = Invoke-NativeCommand -FilePath $winswPath -Arguments @("stop")
    if ($stopCode -ne 0) {
      Write-Log "WinSW stop returned code=$stopCode, fallback to Stop-Service"
    }
  }
  if ((Get-Service -Name $ServiceName -ErrorAction SilentlyContinue).Status -ne "Stopped") {
    try {
      Stop-Service -Name $ServiceName -Force -ErrorAction Stop
    } catch {
      Write-Log "Stop-Service failed, fallback to sc.exe stop: $($_.Exception.Message)"
      $stopCode = Invoke-NativeCommand -FilePath "sc.exe" -Arguments @("stop", $ServiceName)
      if ($stopCode -ne 0) {
        Write-Log "sc.exe stop returned code=$stopCode"
      }
    }
  }
  if (-not (Wait-ServiceStatus -Name $ServiceName -Status "Stopped" -TimeoutSec $StopTimeoutSec)) {
    $latest = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    $status = if ($latest) { [string]$latest.Status } else { "missing" }
    Write-ServiceDiagnostics -Name $ServiceName -WinSWPath $winswPath
    Fail "服务停止超时：$ServiceName status=$status"
  }
}

if (-not $KeepOrphanProcess) {
  Stop-OrphanProbeNodeProcesses -ProbeExePath $probeExePath -ServiceName $ServiceName
}

if ($StopOnly) {
  Write-Log "service stopped and orphan processes cleaned: $ServiceName"
  Write-ServiceDiagnostics -Name $ServiceName -WinSWPath $winswPath
  exit 0
}

Write-Log "starting service"
if ($winswPath) {
  $startCode = Invoke-NativeCommand -FilePath $winswPath -Arguments @("start")
  if ($startCode -ne 0) {
    Write-Log "WinSW start returned code=$startCode, fallback to Start-Service"
  }
}
if ((Get-Service -Name $ServiceName -ErrorAction SilentlyContinue).Status -ne "Running") {
  try {
    Start-Service -Name $ServiceName -ErrorAction Stop
  } catch {
    Write-Log "Start-Service failed, fallback to sc.exe start: $($_.Exception.Message)"
    $startCode = Invoke-NativeCommand -FilePath "sc.exe" -Arguments @("start", $ServiceName)
    if ($startCode -ne 0) {
      Write-Log "sc.exe start returned code=$startCode"
    }
  }
}

if (-not (Wait-ServiceStatus -Name $ServiceName -Status "Running" -TimeoutSec $StartTimeoutSec)) {
  $latest = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  $status = if ($latest) { [string]$latest.Status } else { "missing" }
  Write-ServiceDiagnostics -Name $ServiceName -WinSWPath $winswPath
  Fail "服务启动超时：$ServiceName status=$status"
}

Write-Log "service restarted: $ServiceName"
