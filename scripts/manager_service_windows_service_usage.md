# manager_service Windows 服务脚本使用说明

## 目标
- 以 Windows 服务方式部署 `manager_service`
- 默认安装目录：`C:\Tools\CloudManager\`
- 数据目录固定：`<InstallRoot>\data`（由服务程序自身强制使用）
- 默认安装源：GitHub Release latest
- 固定资产名：`cloudhelper-manager-service-windows-amd64.exe`

## 脚本清单
- 安装：`scripts/install_manager_service_windows.ps1`
- 卸载：`scripts/uninstall_manager_service_windows.ps1`
- 升级：`scripts/update_manager_service_windows.ps1`

> 说明：安装脚本在服务已存在时会自动执行“停服务→删服务→重建服务”，无需再传 `-Force`。
> `-Force` 参数仅保留兼容性，不再影响该行为。

## 前置条件
1. 使用“管理员权限”打开 PowerShell
2. 机器可访问 GitHub Release
3. 机器可访问本机主控（默认 `http://127.0.0.1:15030`）

## 安装（默认 latest）
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\install_manager_service_windows.ps1
```

## 安装（指定版本 tag）
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\install_manager_service_windows.ps1 \
  -Version v1.2.3
```

## 安装参数说明
- `-InstallRoot` 默认 `C:\Tools\CloudManager\`
- `-GitHubRepo` 默认 `fengzhanhuaer/CloudHelper`
- `-AssetName` 默认 `cloudhelper-manager-service-windows-amd64.exe`
- `-Version` 可选，支持 `v1.2.3` 或 `1.2.3`，未传则下载 latest
- `-BinaryPath` 可选，本地二进制覆盖（调试/离线兜底）
- `-Force` 兼容参数（当前安装脚本默认即会重装已存在服务）

完整示例：
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\install_manager_service_windows.ps1 \
  -InstallRoot 'C:\Tools\CloudManager\' \
  -GitHubRepo 'fengzhanhuaer/CloudHelper' \
  -AssetName 'cloudhelper-manager-service-windows-amd64.exe' \
  -Version 'v1.2.3' \
  -ControllerURL 'http://127.0.0.1:15030'
```

## 卸载
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\uninstall_manager_service_windows.ps1
```

卸载并清理安装目录：
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\uninstall_manager_service_windows.ps1 -PurgeInstallDir
```

## 升级（默认 latest）
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\update_manager_service_windows.ps1
```

## 升级（指定版本 tag）
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\update_manager_service_windows.ps1 \
  -Version v1.2.3
```

升级参数说明：
- `-GitHubRepo` `-AssetName` `-Version` 与安装脚本一致
- `-NewBinaryPath` 可选，本地二进制覆盖（调试/离线兜底）

说明：
- 升级脚本会先停服务，再备份旧二进制为 `manager_service.exe.bak`。
- 替换新版本后重启，若启动失败会自动回滚并尝试恢复服务。

## 验证
```powershell
sc.exe query CloudManagerService
```

```powershell
curl http://127.0.0.1:16033/healthz
```
