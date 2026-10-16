param(
    [string]$DownloadsRoot = 'D:\Code\RouterFirmware\downloads',
    [string]$SourceZipPath = 'D:\迅雷下载\GPL_RT_AX55_300438653297.zip'
)

$ErrorActionPreference = 'Stop'
$firmwareRoot = Split-Path $PSScriptRoot -Parent
$lock = Get-Content -Raw (Join-Path $firmwareRoot 'baseline.lock.json') | ConvertFrom-Json
New-Item -ItemType Directory -Force -Path $DownloadsRoot | Out-Null

function Invoke-BaselineGit {
    param([string]$Cache, [string[]]$GitArguments)
    & git --git-dir $Cache @GitArguments
    if ($LASTEXITCODE -ne 0) { throw "Git failed for $Cache" }
}

& python (Join-Path $PSScriptRoot 'import-official-source.py') $SourceZipPath
if ($LASTEXITCODE -ne 0) { throw 'Official source import failed' }

# The GPL supplies the main SDK; retain only the legacy HND helper input.
foreach ($kind in @('toolchains')) {
    $spec = $lock.auxiliary_toolchains
    $cacheName = if ($kind -eq 'source') { 'asuswrt-bcm-ready.git' } else { 'bcmhnd-toolchains-partial.git' }
    $archiveName = if ($kind -eq 'source') { 'source-baseline.tar' } else { 'toolchains-baseline.tar' }
    $cache = Join-Path $DownloadsRoot $cacheName
    $archive = Join-Path $DownloadsRoot $archiveName
    if (-not (Test-Path (Join-Path $cache 'HEAD'))) {
        & git init --bare $cache
        if ($LASTEXITCODE -ne 0) { throw 'Cannot initialize download cache' }
        Invoke-BaselineGit $cache @('remote', 'add', 'origin', $spec.url)
        Invoke-BaselineGit $cache @('config', 'remote.origin.promisor', 'true')
        Invoke-BaselineGit $cache @('config', 'remote.origin.partialclonefilter', 'blob:none')
    }
    $actual = & git --git-dir $cache rev-parse --verify --quiet HEAD
    if ($LASTEXITCODE -ne 0 -or $actual -ne $spec.commit) {
        Invoke-BaselineGit $cache @('-c', 'http.version=HTTP/1.1', 'fetch', '--depth=1', '--filter=blob:none', 'origin', "$($spec.commit):refs/heads/master")
        Invoke-BaselineGit $cache @('symbolic-ref', 'HEAD', 'refs/heads/master')
    }
    if (Test-Path $archive) {
        if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -eq $spec.archive_sha256) {
            Write-Host "Verified existing $archiveName"
            continue
        }
        throw "Existing archive checksum mismatch: $archive"
    }
    & python (Join-Path $PSScriptRoot 'fetch_baseline.py') $kind $cache $archive
    if ($LASTEXITCODE -ne 0) { throw "Download/export failed: $kind" }
    if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $spec.archive_sha256) {
        throw "Exported archive checksum mismatch: $kind"
    }
}
